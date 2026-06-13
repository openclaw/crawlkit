package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	TurboVecPythonEnv           = "CRAWLKIT_TURBOVEC_PYTHON"
	DefaultTurboVecBitWidth     = 4
	DefaultTurboVecTimeout      = 30 * time.Second
	DefaultTurboVecMaxInputSize = int64(64 << 20)
	DefaultTurboVecMaxOutput    = int64(4 << 20)
	MaxTurboVecDimensions       = 8192
	turboVecMaxTieCandidates    = 16 * 1024
)

type TurboVecOptions struct {
	BitWidth       int
	Command        []string
	Python         string
	Timeout        time.Duration
	MaxInputBytes  int64
	MaxOutputBytes int64
}

type turboVecRequest struct {
	Dimensions int         `json:"dimensions"`
	BitWidth   int         `json:"bit_width"`
	Limit      int         `json:"limit"`
	Query      []float32   `json:"query"`
	Vectors    [][]float32 `json:"vectors"`
}

type turboVecResponse struct {
	Results []turboVecResult `json:"results"`
}

type turboVecResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

func turboVecSearch[T any](ctx context.Context, query []float32, candidates []SearchCandidate[T], opts SearchOptions[T]) ([]SearchResult[T], error) {
	if len(query)%8 != 0 {
		return nil, fmt.Errorf("turbovec dimensions must be a positive multiple of 8, got %d", len(query))
	}
	if len(query) > MaxTurboVecDimensions {
		return nil, fmt.Errorf("turbovec dimensions must be <= %d, got %d", MaxTurboVecDimensions, len(query))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	indexed := make([]SearchCandidate[T], 0, len(candidates))
	vectors := make([][]float32, 0, len(candidates))
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateSearchVector(candidate.Vector, len(query), "candidate", true); err != nil {
			if opts.InvalidVector == InvalidVectorSkip {
				continue
			}
			return nil, err
		}
		indexed = append(indexed, candidate)
		vectors = append(vectors, candidate.Vector)
	}
	if len(vectors) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	maxInput := effectiveTurboVecMaxInput(opts.TurboVec.MaxInputBytes)
	if err := preflightTurboVecInput(len(query), len(vectors), maxInput); err != nil {
		return nil, err
	}
	bridgeQuery := normalizeVector(query)
	bridgeVectors := make([][]float32, len(vectors))
	for i, vector := range vectors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bridgeVectors[i] = normalizeVector(vector)
	}
	if err := validateTurboVecPayloadVector(bridgeQuery, "query"); err != nil {
		return nil, err
	}
	for i, vector := range bridgeVectors {
		if err := validateTurboVecPayloadVector(vector, fmt.Sprintf("candidate %d", i)); err != nil {
			return nil, err
		}
	}
	bitWidth := opts.TurboVec.BitWidth
	if bitWidth == 0 {
		bitWidth = DefaultTurboVecBitWidth
	}
	if bitWidth != 2 && bitWidth != 3 && bitWidth != 4 {
		return nil, fmt.Errorf("turbovec bit width must be 2, 3, or 4, got %d", bitWidth)
	}
	resultLimit, err := turboVecResultLimit(len(bridgeVectors), opts.Limit, opts.TieLess != nil)
	if err != nil {
		return nil, err
	}
	response, err := runTurboVec(ctx, opts.TurboVec, turboVecRequest{
		Dimensions: len(query),
		BitWidth:   bitWidth,
		Limit:      resultLimit,
		Query:      bridgeQuery,
		Vectors:    bridgeVectors,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult[T], 0, min(len(response.Results), opts.Limit))
	seen := make(map[int]struct{}, len(response.Results))
	for _, result := range response.Results {
		if result.Index < 0 || result.Index >= len(indexed) {
			return nil, fmt.Errorf("turbovec returned candidate index %d outside 0..%d", result.Index, len(indexed)-1)
		}
		if _, ok := seen[result.Index]; ok {
			return nil, fmt.Errorf("turbovec returned duplicate candidate index %d", result.Index)
		}
		seen[result.Index] = struct{}{}
		if !validScore(result.Score, opts.MinScore) {
			continue
		}
		out = append(out, SearchResult[T]{
			Item:  indexed[result.Index].Item,
			Score: result.Score,
		})
	}
	sortSearchResults(out, opts.TieLess)
	if len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func turboVecResultLimit(candidateCount, requestedLimit int, needsTieOverfetch bool) (int, error) {
	if requestedLimit <= 0 || requestedLimit > candidateCount {
		requestedLimit = candidateCount
	}
	if !needsTieOverfetch {
		return requestedLimit, nil
	}
	if candidateCount > turboVecMaxTieCandidates {
		return 0, fmt.Errorf("turbovec TieLess requires fetching all %d candidates; use the exact backend or omit TieLess for large candidate sets", candidateCount)
	}
	return candidateCount, nil
}

func normalizeVector(values []float32) []float32 {
	norm := Norm(values)
	out := make([]float32, len(values))
	for i, value := range values {
		out[i] = float32(float64(value) / norm)
	}
	return out
}

func validateTurboVecPayloadVector(values []float32, name string) error {
	for i, value := range values {
		if value <= -1e16 || value >= 1e16 {
			return fmt.Errorf("turbovec %s vector value at index %d has magnitude >= 1e16", name, i)
		}
	}
	return nil
}

func runTurboVec(ctx context.Context, opts TurboVecOptions, request turboVecRequest) (turboVecResponse, error) {
	maxInput := effectiveTurboVecMaxInput(opts.MaxInputBytes)
	if err := preflightTurboVecInput(request.Dimensions, len(request.Vectors), maxInput); err != nil {
		return turboVecResponse{}, err
	}
	maxOutput := effectiveTurboVecMaxOutput(opts.MaxOutputBytes)
	if err := preflightTurboVecOutput(request.Limit, maxOutput); err != nil {
		return turboVecResponse{}, err
	}
	var payload limitedBuffer
	if maxInput > 0 {
		payload.limit = maxInput
	}
	if err := json.NewEncoder(&payload).Encode(request); err != nil {
		return turboVecResponse{}, fmt.Errorf("marshal turbovec request: %w", err)
	}
	if payload.truncated {
		return turboVecResponse{}, fmt.Errorf("turbovec request is over max %d bytes", maxInput)
	}
	runCtx, cancel := turboVecContext(ctx, opts.Timeout)
	defer cancel()
	command, defaultCommand, err := turboVecCommand(opts)
	if err != nil {
		return turboVecResponse{}, err
	}

	cmd := exec.CommandContext(runCtx, command[0], command[1:]...)
	cmd.WaitDelay = 2 * time.Second
	if defaultCommand {
		dir, err := os.MkdirTemp("", "crawlkit-turbovec-*")
		if err != nil {
			return turboVecResponse{}, fmt.Errorf("create turbovec working dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		cmd.Dir = dir
	}
	cmd.Stdin = bytes.NewReader(payload.Bytes())
	var stdout, stderr limitedBuffer
	stdout.limit = maxOutput
	stderr.limit = maxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctxErr := runCtx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return turboVecResponse{}, fmt.Errorf("run turbovec bridge: timed out after %s: %w", effectiveTurboVecTimeout(opts.Timeout), ctxErr)
		}
		return turboVecResponse{}, fmt.Errorf("run turbovec bridge: %w", ctxErr)
	}
	if stderr.truncated {
		return turboVecResponse{}, errors.New("run turbovec bridge: stderr exceeded output limit")
	}
	if stdout.truncated {
		return turboVecResponse{}, errors.New("run turbovec bridge: stdout exceeded output limit")
	}
	if err != nil {
		return turboVecResponse{}, fmt.Errorf("run turbovec bridge: %w: %s", err, firstLine(stderr.String()))
	}
	var response turboVecResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return turboVecResponse{}, fmt.Errorf("decode turbovec response: %w", err)
	}
	return response, nil
}

func effectiveTurboVecMaxInput(maxInput int64) int64 {
	if maxInput == 0 {
		return DefaultTurboVecMaxInputSize
	}
	return maxInput
}

func preflightTurboVecInput(dimensions, vectorCount int, maxInput int64) error {
	if maxInput <= 0 || dimensions <= 0 || vectorCount <= 0 {
		return nil
	}
	const floatJSONBudget = int64(16)
	coordinates := int64(dimensions) * int64(vectorCount+1)
	estimate := int64(256) + coordinates*floatJSONBudget + int64(vectorCount*4)
	if estimate > maxInput {
		return fmt.Errorf("turbovec request estimate is %d bytes, over max %d", estimate, maxInput)
	}
	return nil
}

func effectiveTurboVecMaxOutput(maxOutput int64) int64 {
	if maxOutput == 0 {
		return DefaultTurboVecMaxOutput
	}
	return maxOutput
}

func preflightTurboVecOutput(limit int, maxOutput int64) error {
	if maxOutput <= 0 || limit <= 0 {
		return nil
	}
	const resultJSONBudget = int64(48)
	estimate := int64(32) + int64(limit)*resultJSONBudget
	if estimate > maxOutput {
		return fmt.Errorf("turbovec response estimate is %d bytes, over max %d", estimate, maxOutput)
	}
	return nil
}

func turboVecContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout < 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, effectiveTurboVecTimeout(timeout))
}

func effectiveTurboVecTimeout(timeout time.Duration) time.Duration {
	if timeout == 0 {
		return DefaultTurboVecTimeout
	}
	return timeout
}

func turboVecCommand(opts TurboVecOptions) ([]string, bool, error) {
	if len(opts.Command) > 0 {
		return opts.Command, false, nil
	}
	python := opts.Python
	if python == "" {
		python = os.Getenv(TurboVecPythonEnv)
	}
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			python, err = exec.LookPath("python")
		}
		if err != nil {
			return nil, false, fmt.Errorf("find Python for turbovec bridge: %w; set %s or TurboVecOptions.Command", err, TurboVecPythonEnv)
		}
	}
	python, err := resolvePythonPath(python)
	if err != nil {
		return nil, false, err
	}
	return []string{python, "-E", "-c", turboVecBridgeScript}, true, nil
}

func resolvePythonPath(python string) (string, error) {
	if filepath.IsAbs(python) {
		return python, nil
	}
	if filepath.Base(python) == python {
		resolved, err := exec.LookPath(python)
		if err != nil {
			return "", fmt.Errorf("find Python %q: %w", python, err)
		}
		return resolved, nil
	}
	return "", fmt.Errorf("Python path %q must be absolute or resolved from PATH", python)
}

func firstLine(value string) string {
	for i, r := range value {
		if r == '\n' || r == '\r' {
			return value[:i]
		}
	}
	return value
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return b.buf.Write(p)
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.truncated = true
		_, _ = b.buf.Write(p[:remaining])
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

const turboVecBridgeScript = `
import sys
sys.path = [p for p in sys.path if p not in ("", ".")]
import os
_cwd = os.getcwd()
sys.path = [p for p in sys.path if p and os.path.abspath(p) != _cwd]

import json
import math

try:
    import numpy as np
    from turbovec import IdMapIndex
except Exception as exc:
    print("install the Python turbovec package to use the turbovec vector backend: %s" % exc, file=sys.stderr)
    sys.exit(3)

req = json.load(sys.stdin)
dim = int(req["dimensions"])
bit_width = int(req.get("bit_width") or 4)
limit = int(req.get("limit") or 20)
vectors = np.asarray(req["vectors"], dtype=np.float32)
query = np.asarray(req["query"], dtype=np.float32)
if dim <= 0 or dim % 8 != 0:
    raise ValueError("dimensions must be a positive multiple of 8")
if vectors.ndim != 2 or vectors.shape[1] != dim:
    raise ValueError("vector matrix shape does not match dimensions")
if query.ndim != 1 or query.shape[0] != dim:
    raise ValueError("query shape does not match dimensions")
if not np.all(np.isfinite(vectors)) or not np.all(np.isfinite(query)):
    raise ValueError("vectors must contain only finite values")

index = IdMapIndex(dim=dim, bit_width=bit_width)
ids = np.arange(vectors.shape[0], dtype=np.uint64)
index.add_with_ids(vectors, ids)
try:
    scores, found = index.search(query, k=limit)
except TypeError:
    scores, found = index.search(query.reshape(1, dim), k=limit)
scores = np.asarray(scores).reshape(-1)
found = np.asarray(found).reshape(-1)
results = []
for score, idx in zip(scores, found):
    if int(idx) < 0 or not math.isfinite(float(score)):
        continue
    results.append({"index": int(idx), "score": float(score)})
print(json.dumps({"results": results}, separators=(",", ":")))
`

var _ io.Writer = (*limitedBuffer)(nil)
