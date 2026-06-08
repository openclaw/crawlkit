package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

const TurboVecPythonEnv = "CRAWLKIT_TURBOVEC_PYTHON"

type TurboVecOptions struct {
	BitWidth int
	Command  []string
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
	if len(query) == 0 {
		return nil, errors.New("query vector is empty")
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	indexed := make([]SearchCandidate[T], 0, len(candidates))
	vectors := make([][]float32, 0, len(candidates))
	for _, candidate := range candidates {
		if err := ValidateDimensions(candidate.Vector, len(query)); err != nil {
			if opts.InvalidVector == InvalidVectorSkip {
				continue
			}
			return nil, err
		}
		indexed = append(indexed, candidate)
		vectors = append(vectors, candidate.Vector)
	}
	if len(vectors) == 0 {
		return nil, nil
	}
	bitWidth := opts.TurboVec.BitWidth
	if bitWidth == 0 {
		bitWidth = 4
	}
	if bitWidth != 2 && bitWidth != 4 {
		return nil, fmt.Errorf("turbovec bit width must be 2 or 4, got %d", bitWidth)
	}
	response, err := runTurboVec(ctx, opts.TurboVec, turboVecRequest{
		Dimensions: len(query),
		BitWidth:   bitWidth,
		Limit:      opts.Limit,
		Query:      query,
		Vectors:    vectors,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult[T], 0, len(response.Results))
	for _, result := range response.Results {
		if result.Index < 0 || result.Index >= len(indexed) {
			return nil, fmt.Errorf("turbovec returned candidate index %d outside 0..%d", result.Index, len(indexed)-1)
		}
		if !validScore(result.Score, opts.MinScore) {
			continue
		}
		out = append(out, SearchResult[T]{
			Item:  indexed[result.Index].Item,
			Score: result.Score,
		})
		if len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}

func runTurboVec(ctx context.Context, opts TurboVecOptions, request turboVecRequest) (turboVecResponse, error) {
	command := opts.Command
	if len(command) == 0 {
		command = defaultTurboVecCommand()
	}
	if len(command) == 0 {
		return turboVecResponse{}, errors.New("turbovec command is empty")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return turboVecResponse{}, fmt.Errorf("marshal turbovec request: %w", err)
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return turboVecResponse{}, fmt.Errorf("run turbovec bridge: %w: %s", err, firstLine(stderr.String()))
	}
	var response turboVecResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return turboVecResponse{}, fmt.Errorf("decode turbovec response: %w", err)
	}
	return response, nil
}

func defaultTurboVecCommand() []string {
	if python := os.Getenv(TurboVecPythonEnv); python != "" {
		return []string{python, "-c", turboVecBridgeScript}
	}
	return []string{"python3", "-c", turboVecBridgeScript}
}

func firstLine(value string) string {
	for i, r := range value {
		if r == '\n' || r == '\r' {
			return value[:i]
		}
	}
	return value
}

const turboVecBridgeScript = `
import json
import sys

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
if vectors.ndim != 2 or vectors.shape[1] != dim:
    raise ValueError("vector matrix shape does not match dimensions")
if query.ndim != 1 or query.shape[0] != dim:
    raise ValueError("query shape does not match dimensions")

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
    results.append({"index": int(idx), "score": float(score)})
print(json.dumps({"results": results}, separators=(",", ":")))
`
