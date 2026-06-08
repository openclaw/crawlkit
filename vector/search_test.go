package vector

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSearchExact(t *testing.T) {
	minScore := 0.01
	results, err := Search(t.Context(), []float32{1, 0}, []SearchCandidate[string]{
		{Item: "b", Vector: []float32{0.5, 0}},
		{Item: "a", Vector: []float32{0.5, 0}},
		{Item: "c", Vector: []float32{0, 1}},
		{Item: "bad", Vector: []float32{1}},
	}, SearchOptions[string]{
		Limit:         2,
		MinScore:      &minScore,
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

func TestSearchTurboVecBridge(t *testing.T) {
	t.Setenv("CRAWLKIT_TEST_TURBOVEC_HELPER", "1")
	results, err := Search(t.Context(), []float32{1, 0}, []SearchCandidate[string]{
		{Item: "first", Vector: []float32{1, 0}},
		{Item: "second", Vector: []float32{0.8, 0.2}},
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
}

func TestTurboVecHelperProcess(t *testing.T) {
	if os.Getenv("CRAWLKIT_TEST_TURBOVEC_HELPER") != "1" {
		return
	}
	defer os.Exit(0)

	var request turboVecRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		panic(err)
	}
	if request.Dimensions != 2 || request.BitWidth != 4 || request.Limit != 2 || len(request.Vectors) != 2 {
		panic("unexpected turbovec request")
	}
	response := turboVecResponse{Results: []turboVecResult{
		{Index: 1, Score: 0.9},
		{Index: 0, Score: 0.8},
	}}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		panic(err)
	}
}
