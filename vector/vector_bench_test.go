package vector

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

var benchmarkScore float64
var benchmarkBlob []byte
var benchmarkVector []float32
var benchmarkResults []SearchResult[int]

func benchmarkUnitVector(rng *rand.Rand, dimensions int) []float32 {
	values := make([]float32, dimensions)
	var sum float64
	for i := range values {
		values[i] = float32(rng.NormFloat64())
		sum += float64(values[i]) * float64(values[i])
	}
	norm := math.Sqrt(sum)
	for i := range values {
		values[i] = float32(float64(values[i]) / norm)
	}
	return values
}

func BenchmarkVector(b *testing.B) {
	for _, dimensions := range []int{768, 1536, 3072} {
		b.Run(fmt.Sprint(dimensions), func(b *testing.B) {
			rng := rand.New(rand.NewPCG(1, 2))
			query := benchmarkUnitVector(rng, dimensions)
			candidate := benchmarkUnitVector(rng, dimensions)
			queryNorm := Norm(query)
			blob, err := EncodeFloat32(candidate)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("Encode", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkBlob, err = EncodeFloat32(candidate)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Decode", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkVector, err = DecodeFloat32(blob)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Norm", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkScore = Norm(candidate)
				}
			})
			b.Run("Cosine", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkScore, err = CosineSimilarity(query, queryNorm, candidate)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// Mirrors Discrawl's exact-search numeric loop, including its zero-norm check.
func BenchmarkDecodeScore10000x1536(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 2))
	query := benchmarkUnitVector(rng, 1536)
	queryNorm := Norm(query)
	blobs := make([][]byte, 10000)
	for i := range blobs {
		var err error
		blobs[i], err = EncodeFloat32(benchmarkUnitVector(rng, 1536))
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		var total float64
		for _, blob := range blobs {
			candidate, err := DecodeFloat32(blob)
			if err != nil {
				b.Fatal(err)
			}
			if Norm(candidate) == 0 {
				b.Fatal("zero vector")
			}
			score, err := CosineSimilarity(query, queryNorm, candidate)
			if err != nil {
				b.Fatal(err)
			}
			total += score
		}
		benchmarkScore = total
	}
}

func BenchmarkSearchExact10000x1536(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 2))
	query := benchmarkUnitVector(rng, 1536)
	candidates := make([]SearchCandidate[int], 10000)
	for i := range candidates {
		candidates[i] = SearchCandidate[int]{Item: i, Vector: benchmarkUnitVector(rng, 1536)}
	}
	opts := SearchOptions[int]{Backend: BackendExact, Limit: 20, TieLess: func(a, b int) bool { return a < b }}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		var err error
		benchmarkResults, err = Search(ctx, query, candidates, opts)
		if err != nil {
			b.Fatal(err)
		}
	}
}
