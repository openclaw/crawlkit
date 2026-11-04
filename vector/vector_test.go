package vector

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestCosineSimilarityAndDimensions(t *testing.T) {
	require.NoError(t, ValidateDimensions([]float32{1, 2}, 2))
	require.ErrorContains(t, ValidateDimensions([]float32{1}, 2), "dimensions mismatch")
	require.ErrorContains(t, ValidateDimensions([]float32{1}, 0), "positive")

	query := []float32{1, 0}
	score, err := CosineSimilarity(query, Norm(query), []float32{0.5, 0})
	require.NoError(t, err)
	checkKernelNumber(t, "cosine", score, 1, 0.0001)

	_, err = CosineSimilarity(query, 0, []float32{1, 0})
	require.ErrorContains(t, err, "query vector is zero")
	_, err = CosineSimilarity(query, Norm(query), []float32{0, 0})
	require.ErrorContains(t, err, "candidate vector is zero")
	_, err = CosineSimilarity(query, Norm(query), []float32{1})
	require.ErrorContains(t, err, "dimensions mismatch")
	require.Equal(t, math.Sqrt(5), Norm([]float32{1, 2}))
}

func TestTopK(t *testing.T) {
	items := []Scored[string]{
		{Item: "c", Score: 0.3},
		{Item: "a", Score: 0.5},
		{Item: "b", Score: 0.5},
	}
	top := TopK(items, 2, func(left, right string) bool { return left < right })
	require.Equal(t, []Scored[string]{{Item: "a", Score: 0.5}, {Item: "b", Score: 0.5}}, top)
	if got := TopK(items, 0, nil); got != nil {
		t.Fatalf("TopK with zero limit = %v, want nil", got)
	}
}

func TestReciprocalRankFusion(t *testing.T) {
	rankings := [][]string{
		{"a", "b"},
		{"b", "c"},
	}
	ids := []func(string) string{
		func(value string) string { return value },
		func(value string) string { return value },
	}
	results := ReciprocalRankFusion(rankings, ids, []float64{1, 1}, 60)
	require.Len(t, results, 3)
	require.Equal(t, "b", results[0].Item)
	if !(results[0].Score > results[1].Score) {
		t.Fatalf("leading score %g must exceed next score %g", results[0].Score, results[1].Score)
	}
}

func TestReciprocalRankFusionPreservesFirstSeenOrderForTies(t *testing.T) {
	rankings := [][]string{
		{"a"},
		{"b"},
	}
	ids := []func(string) string{
		func(value string) string { return value },
		func(value string) string { return value },
	}
	results := ReciprocalRankFusion(rankings, ids, []float64{1, 1}, 60)
	require.Len(t, results, 2)
	require.Equal(t, "a", results[0].Item)
	require.Equal(t, "b", results[1].Item)
	require.Equal(t, results[0].Score, results[1].Score)
}

type requireAPI struct{}

var require requireAPI

func (requireAPI) NoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func (requireAPI) Equal(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("not equal:\nwant: %#v\n got: %#v", want, got)
	}
}

func (requireAPI) Len(t *testing.T, value any, want int) {
	t.Helper()
	got := reflect.ValueOf(value).Len()
	if got != want {
		t.Fatalf("len mismatch: got %d want %d", got, want)
	}
}

func (requireAPI) ErrorContains(t *testing.T, err error, needle string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", needle)
	}
	if !strings.Contains(err.Error(), needle) {
		t.Fatalf("expected error containing %q, got %q", needle, err.Error())
	}
}

func (requireAPI) ErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("expected error %v to match %v", err, target)
	}
}
