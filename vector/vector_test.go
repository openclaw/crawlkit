package vector

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestFloat32EncodingRoundTrip(t *testing.T) {
	blob, err := EncodeFloat32([]float32{1, -2.5, 3.25})
	require.NoError(t, err)
	require.Len(t, blob, 12)

	values, err := DecodeFloat32(blob)
	require.NoError(t, err)
	require.Equal(t, []float32{1, -2.5, 3.25}, values)

	_, err = DecodeFloat32([]byte{1, 2, 3})
	require.ErrorContains(t, err, "not a multiple of 4")
}

func TestCosineSimilarityAndDimensions(t *testing.T) {
	require.NoError(t, ValidateDimensions([]float32{1, 2}, 2))
	require.ErrorContains(t, ValidateDimensions([]float32{1}, 2), "dimensions mismatch")
	require.ErrorContains(t, ValidateDimensions([]float32{1}, 0), "positive")

	query := []float32{1, 0}
	score, err := CosineSimilarity(query, Norm(query), []float32{0.5, 0})
	require.NoError(t, err)
	require.InDelta(t, 1, score, 0.0001)

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
	require.Nil(t, TopK(items, 0, nil))
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
	require.Greater(t, results[0].Score, results[1].Score)
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

func (requireAPI) Nil(t *testing.T, value any) {
	t.Helper()
	if !isNil(value) {
		t.Fatalf("expected nil, got %#v", value)
	}
}

func (requireAPI) Greater(t *testing.T, left, right float64) {
	t.Helper()
	if left <= right {
		t.Fatalf("expected %v > %v", left, right)
	}
}

func (requireAPI) InDelta(t *testing.T, want, got, delta float64) {
	t.Helper()
	diff := math.Abs(want - got)
	if diff > delta {
		t.Fatalf("not within delta: want %v got %v delta %v", want, got, delta)
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

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
