package vector

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
)

const DefaultRRFK = 60.0

type SearchBackend = string

const (
	BackendExact    SearchBackend = "exact"
	BackendTurboVec SearchBackend = "turbovec"
)

type Scored[T any] struct {
	Item  T
	Score float64
}

type RRFEntry[T any] struct {
	Item  T
	Score float64
}

func EncodeFloat32(values []float32) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(values)*4))
	for _, value := range values {
		if err := binary.Write(buf, binary.LittleEndian, value); err != nil {
			return nil, fmt.Errorf("encode float32 vector: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func DecodeFloat32(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("float32 vector blob length %d is not a multiple of 4", len(blob))
	}
	out := make([]float32, len(blob)/4)
	reader := bytes.NewReader(blob)
	for i := range out {
		if err := binary.Read(reader, binary.LittleEndian, &out[i]); err != nil {
			return nil, fmt.Errorf("decode float32 vector: %w", err)
		}
	}
	return out, nil
}

func ValidateDimensions(values []float32, dimensions int) error {
	if dimensions <= 0 {
		return errors.New("dimensions must be positive")
	}
	if len(values) != dimensions {
		return fmt.Errorf("dimensions mismatch: got %d want %d", len(values), dimensions)
	}
	return nil
}

func Norm(values []float32) float64 {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	return math.Sqrt(sum)
}

func CosineSimilarity(query []float32, queryNorm float64, candidate []float32) (float64, error) {
	if len(candidate) != len(query) {
		return 0, fmt.Errorf("dimensions mismatch: got %d want %d", len(candidate), len(query))
	}
	if queryNorm == 0 {
		return 0, errors.New("query vector is zero")
	}
	candidateNorm := Norm(candidate)
	if candidateNorm == 0 {
		return 0, errors.New("candidate vector is zero")
	}
	var dot float64
	for i := range query {
		dot += float64(query[i]) * float64(candidate[i])
	}
	return dot / (queryNorm * candidateNorm), nil
}

func TopK[T any](items []Scored[T], limit int, tieLess func(left, right T) bool) []Scored[T] {
	if limit <= 0 || len(items) == 0 {
		return nil
	}
	sorted := append([]Scored[T](nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		if tieLess == nil {
			return false
		}
		return tieLess(sorted[i].Item, sorted[j].Item)
	})
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	return sorted
}

func ReciprocalRankFusion[T any](rankings [][]T, ids []func(T) string, weights []float64, k float64) []RRFEntry[T] {
	if k <= 0 {
		k = DefaultRRFK
	}
	entries := map[string]*RRFEntry[T]{}
	for rankingIndex, ranking := range rankings {
		weight := 1.0
		if rankingIndex < len(weights) && weights[rankingIndex] != 0 {
			weight = weights[rankingIndex]
		}
		var idFn func(T) string
		if rankingIndex < len(ids) {
			idFn = ids[rankingIndex]
		}
		for index, item := range ranking {
			if idFn == nil {
				continue
			}
			id := idFn(item)
			if id == "" {
				continue
			}
			entry := entries[id]
			if entry == nil {
				entry = &RRFEntry[T]{Item: item}
				entries[id] = entry
			}
			entry.Score += weight / (k + float64(index+1))
		}
	}
	out := make([]RRFEntry[T], 0, len(entries))
	for _, entry := range entries {
		out = append(out, *entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}
