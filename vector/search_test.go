package vector

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchExact(t *testing.T) {
	minScore := 0.01
	results, err := Search(t.Context(), []float32{1, 0}, []SearchCandidate[string]{
		{Item: "b", Vector: []float32{0.5, 0}},
		{Item: "a", Vector: []float32{0.5, 0}},
		{Item: "c", Vector: []float32{0, 1}},
		{Item: "excluded", Vector: []float32{1, 0}},
		{Item: "bad-dim", Vector: []float32{1}},
		{Item: "bad-zero", Vector: []float32{0, 0}},
		{Item: "bad-float", Vector: []float32{float32(math.NaN()), 0}},
	}, SearchOptions[string]{
		Limit:         2,
		MinScore:      &minScore,
		Exclude:       func(value string) bool { return value == "excluded" },
		TieLess:       func(left, right string) bool { return left < right },
		InvalidVector: InvalidVectorSkip,
	})
	require.NoError(t, err)
	require.Equal(t, []SearchResult[string]{
		{Item: "a", Score: 1},
		{Item: "b", Score: 1},
	}, results)
}

func TestSearchExactErrorsOnInvalidCandidateByDefault(t *testing.T) {
	_, err := Search(t.Context(), []float32{1, 0}, []SearchCandidate[string]{
		{Item: "bad", Vector: []float32{1}},
	}, SearchOptions[string]{})
	require.ErrorContains(t, err, "dimensions mismatch")
}

func TestSearchRejectsInvalidQuery(t *testing.T) {
	_, err := Search(t.Context(), nil, nil, SearchOptions[string]{})
	require.ErrorContains(t, err, "query vector is empty")
	_, err = Search(t.Context(), []float32{0, 0}, nil, SearchOptions[string]{})
	require.ErrorContains(t, err, "query vector is zero")
	_, err = Search(t.Context(), []float32{float32(math.Inf(1)), 0}, nil, SearchOptions[string]{})
	require.ErrorContains(t, err, "non-finite")
}

func TestSearchExactHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Search(ctx, []float32{1, 0}, []SearchCandidate[string]{
		{Item: "first", Vector: []float32{1, 0}},
	}, SearchOptions[string]{})
	require.ErrorContains(t, err, context.Canceled.Error())
}

func TestSearchTurboVecHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Search(ctx, []float32{1, 0, 0, 0, 0, 0, 0, 0}, nil, SearchOptions[string]{
		Backend: BackendTurboVec,
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestSearchTurboVecBridge(t *testing.T) {
	t.Setenv("CRAWLKIT_TEST_TURBOVEC_HELPER", "normal")
	results, err := Search(t.Context(), []float32{2, 0, 0, 0, 0, 0, 0, 0}, []SearchCandidate[string]{
		{Item: "first", Vector: []float32{100, 0, 0, 0, 0, 0, 0, 0}},
		{Item: "second", Vector: []float32{80, 20, 0, 0, 0, 0, 0, 0}},
	}, SearchOptions[string]{
		Backend: BackendTurboVec,
		Limit:   2,
		TurboVec: TurboVecOptions{
			Command: []string{os.Args[0], "-test.run=TestTurboVecHelperProcess", "--"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []SearchResult[string]{
		{Item: "second", Score: 0.9},
		{Item: "first", Score: 0.8},
	}, results)

	_, err = Search(t.Context(), []float32{2, 0, 0, 0, 0, 0, 0, 0}, []SearchCandidate[string]{
		{Item: "first", Vector: []float32{100, 0, 0, 0, 0, 0, 0, 0}},
	}, SearchOptions[string]{
		Backend: BackendTurboVec,
		Limit:   1,
		TurboVec: TurboVecOptions{
			BitWidth: 3,
			Command:  []string{os.Args[0], "-test.run=TestTurboVecHelperProcess", "--"},
		},
	})
	require.NoError(t, err)
}

func TestSearchTurboVecSortsAndBreaksTies(t *testing.T) {
	t.Setenv("CRAWLKIT_TEST_TURBOVEC_HELPER", "unsorted")
	results, err := Search(t.Context(), []float32{1, 0, 0, 0, 0, 0, 0, 0}, []SearchCandidate[string]{
		{Item: "b", Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}},
		{Item: "a", Vector: []float32{0.9, 0.1, 0, 0, 0, 0, 0, 0}},
		{Item: "c", Vector: []float32{0.8, 0.2, 0, 0, 0, 0, 0, 0}},
	}, SearchOptions[string]{
		Backend: BackendTurboVec,
		Limit:   2,
		TieLess: func(left, right string) bool { return left < right },
		TurboVec: TurboVecOptions{
			Command: []string{os.Args[0], "-test.run=TestTurboVecHelperProcess", "--"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []SearchResult[string]{
		{Item: "a", Score: 1},
		{Item: "b", Score: 1},
	}, results)
}

func TestSearchTurboVecRejectsBadBridgeResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "duplicate", want: "duplicate"},
		{name: "out-of-range", want: "outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CRAWLKIT_TEST_TURBOVEC_HELPER", tc.name)
			_, err := Search(t.Context(), []float32{1, 0, 0, 0, 0, 0, 0, 0}, []SearchCandidate[string]{
				{Item: "first", Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}},
				{Item: "second", Vector: []float32{0.8, 0.2, 0, 0, 0, 0, 0, 0}},
			}, SearchOptions[string]{
				Backend: BackendTurboVec,
				Limit:   2,
				TurboVec: TurboVecOptions{
					Command: []string{os.Args[0], "-test.run=TestTurboVecHelperProcess", "--"},
				},
			})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestSearchTurboVecRejectsUnsupportedDimensions(t *testing.T) {
	_, err := Search(t.Context(), []float32{1, 0}, []SearchCandidate[string]{
		{Item: "first", Vector: []float32{1, 0}},
	}, SearchOptions[string]{Backend: BackendTurboVec})
	require.ErrorContains(t, err, "positive multiple of 8")

	tooWide := make([]float32, MaxTurboVecDimensions+8)
	tooWide[0] = 1
	_, err = Search(t.Context(), tooWide, []SearchCandidate[string]{
		{Item: "first", Vector: tooWide},
	}, SearchOptions[string]{Backend: BackendTurboVec})
	require.ErrorContains(t, err, "<= 8192")
}

func TestRunTurboVecLimitsAndErrors(t *testing.T) {
	_, err := runTurboVec(t.Context(), TurboVecOptions{Command: []string{os.Args[0], "-test.run=TestTurboVecHelperProcess", "--"}, MaxInputBytes: 10}, turboVecRequest{
		Dimensions: 8,
		BitWidth:   4,
		Limit:      1,
		Query:      []float32{1, 0, 0, 0, 0, 0, 0, 0},
		Vectors:    [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}},
	})
	require.ErrorContains(t, err, "over max")

	_, err = runTurboVec(t.Context(), TurboVecOptions{Command: []string{os.Args[0], "-test.run=TestTurboVecHelperProcess", "--"}, MaxOutputBytes: 10}, turboVecRequest{
		Dimensions: 8,
		BitWidth:   4,
		Limit:      1,
		Query:      []float32{1, 0, 0, 0, 0, 0, 0, 0},
		Vectors:    [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}},
	})
	require.ErrorContains(t, err, "response estimate")

	t.Setenv("CRAWLKIT_TEST_TURBOVEC_HELPER", "sleep")
	_, err = runTurboVec(t.Context(), TurboVecOptions{Timeout: time.Millisecond, Command: []string{os.Args[0], "-test.run=TestTurboVecHelperProcess", "--"}}, turboVecRequest{
		Dimensions: 8,
		BitWidth:   4,
		Limit:      1,
		Query:      []float32{1, 0, 0, 0, 0, 0, 0, 0},
		Vectors:    [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}},
	})
	require.ErrorContains(t, err, "timed out")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = runTurboVec(t.Context(), TurboVecOptions{Command: []string{"/path/to/missing/turbovec-helper"}}, turboVecRequest{})
	require.ErrorContains(t, err, "run turbovec bridge")
}

func TestTurboVecDefaultCommandUsesSafePythonMode(t *testing.T) {
	command, defaultCommand, err := turboVecCommand(TurboVecOptions{Python: "/usr/bin/python3"})
	require.NoError(t, err)
	require.Equal(t, []string{"/usr/bin/python3", "-E", "-c", turboVecBridgeScript}, command)
	require.Equal(t, true, defaultCommand)
}

func TestTurboVecCommandResolvesRelativePythonBeforeWorkingDirChange(t *testing.T) {
	dir := t.TempDir()
	python := filepath.Join(dir, "python")
	if err := os.WriteFile(python, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write python shim: %v", err)
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	command, defaultCommand, err := turboVecCommand(TurboVecOptions{Python: "./python"})
	require.NoError(t, err)
	wantPython, err := filepath.EvalSymlinks(python)
	require.NoError(t, err)
	gotPython, err := filepath.EvalSymlinks(command[0])
	require.NoError(t, err)
	require.Equal(t, wantPython, gotPython)
	require.Equal(t, []string{"-E", "-c", turboVecBridgeScript}, command[1:])
	require.Equal(t, true, defaultCommand)
}

func TestTurboVecTieLessRequiresBoundedCandidateSet(t *testing.T) {
	candidates := make([]SearchCandidate[string], turboVecMaxTieCandidates+1)
	for i := range candidates {
		candidates[i] = SearchCandidate[string]{Item: "item", Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}
	}
	_, err := Search(t.Context(), []float32{1, 0, 0, 0, 0, 0, 0, 0}, candidates, SearchOptions[string]{
		Backend: BackendTurboVec,
		Limit:   2,
		TieLess: func(left, right string) bool { return left < right },
	})
	require.ErrorContains(t, err, "TieLess requires fetching all")
}

func TestSearchTurboVecRealPython(t *testing.T) {
	if os.Getenv("CRAWLKIT_TURBOVEC_INTEGRATION") != "1" {
		t.Skip("set CRAWLKIT_TURBOVEC_INTEGRATION=1 with turbovec installed")
	}
	results, err := Search(t.Context(), []float32{1, 0, 0, 0, 0, 0, 0, 0}, []SearchCandidate[string]{
		{Item: "first", Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}},
		{Item: "second", Vector: []float32{0.8, 0.2, 0, 0, 0, 0, 0, 0}},
		{Item: "third", Vector: []float32{0, 1, 0, 0, 0, 0, 0, 0}},
	}, SearchOptions[string]{Backend: BackendTurboVec, Limit: 2})
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "first", results[0].Item)
	require.Equal(t, "second", results[1].Item)
}

func TestTurboVecHelperProcess(t *testing.T) {
	mode := os.Getenv("CRAWLKIT_TEST_TURBOVEC_HELPER")
	if mode == "" {
		return
	}
	defer os.Exit(0)
	if mode == "sleep" {
		time.Sleep(time.Second)
	}

	var request turboVecRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		panic(err)
	}
	if request.Dimensions != 8 || (request.BitWidth != 3 && request.BitWidth != 4) || request.Limit < 1 || len(request.Vectors) < 1 {
		panic("unexpected turbovec request")
	}
	requireUnitVector(request.Query)
	for _, vector := range request.Vectors {
		requireUnitVector(vector)
	}
	response := turboVecResponse{}
	switch mode {
	case "normal", "sleep":
		if len(request.Vectors) == 1 {
			response.Results = []turboVecResult{{Index: 0, Score: 0.9}}
		} else {
			response.Results = []turboVecResult{{Index: 1, Score: 0.9}, {Index: 0, Score: 0.8}}
		}
	case "unsorted":
		response.Results = []turboVecResult{{Index: 2, Score: 0.2}, {Index: 1, Score: 1}, {Index: 0, Score: 1}}
	case "duplicate":
		response.Results = []turboVecResult{{Index: 0, Score: 1}, {Index: 0, Score: 0.9}}
	case "out-of-range":
		response.Results = []turboVecResult{{Index: 99, Score: 1}}
	default:
		panic("unknown helper mode")
	}
	if len(response.Results) > request.Limit {
		response.Results = response.Results[:request.Limit]
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		panic(err)
	}
}

func requireUnitVector(values []float32) {
	norm := Norm(values)
	if math.Abs(norm-1) > 0.0001 {
		panic("expected unit vector")
	}
	for _, value := range values {
		if value <= -1e16 || value >= 1e16 {
			panic("turbovec payload magnitude too large")
		}
	}
}
