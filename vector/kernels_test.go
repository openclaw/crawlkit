package vector

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

const cosineTolerance = 1e-6

func checkKernelNumber(t *testing.T, name string, got, want, tolerance float64) {
	t.Helper()
	if math.IsNaN(want) {
		if !math.IsNaN(got) {
			t.Fatalf("%s: got %g, want NaN", name, got)
		}
	} else if math.IsInf(want, 0) {
		if got != want {
			t.Fatalf("%s: got %g, want %g", name, got, want)
		}
	} else if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > tolerance {
		t.Fatalf("%s: got %.17g want %.17g deviation %g tolerance %g", name, got, want, math.Abs(got-want), tolerance)
	}
}

func TestKernelsAgainstScalar(t *testing.T) {
	lengths := make([]int, 3*64+8)
	for i := range lengths {
		lengths[i] = i
	}
	lengths = append(lengths, 255, 256, 257, 511, 512, 513, 768, 1536, 3072)
	rng := rand.New(rand.NewPCG(5, 6))
	var maxDeviation float64
	for _, length := range lengths {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			for _, scale := range []float32{0, 1, 1e-22, 1e-16, 1e15, 1e19, 1e30, math.MaxFloat32} {
				q, c := make([]float32, length), make([]float32, length)
				for i := range q {
					q[i] = float32(rng.Float64()*2-1) * scale
					c[i] = float32(rng.Float64()*2-1) * scale
				}
				for _, pair := range [][2][]float32{{q, c}, {q, q}} {
					q, c := pair[0], pair[1]
					wantDot, wantSum := dotNormScalar(q, c)
					gotDot, gotSum := dotNorm(q, math.Sqrt(normSquaredScalar(q)), c)
					normProduct := math.Sqrt(normSquaredScalar(q)) * math.Sqrt(wantSum)
					checkKernelNumber(t, "normSquared", normSquared(c), wantSum, cosineTolerance*wantSum)
					checkKernelNumber(t, "fused norm", gotSum, wantSum, cosineTolerance*wantSum)
					checkKernelNumber(t, "dot", gotDot, wantDot, cosineTolerance*normProduct)
					if normProduct != 0 {
						got, err := CosineSimilarity(q, Norm(q), c)
						if err != nil {
							t.Fatal(err)
						}
						want := wantDot / normProduct
						checkKernelNumber(t, "cosine", got, want, cosineTolerance)
						maxDeviation = max(maxDeviation, math.Abs(got-want))
					}
				}
			}
			for _, special := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), math.SmallestNonzeroFloat32} {
				for index := range length {
					q, c := make([]float32, length), make([]float32, length)
					for i := range q {
						q[i] = 1
						c[i] = 1
					}
					c[index] = special
					wantDot, wantSum := dotNormScalar(q, c)
					gotDot, gotSum := dotNorm(q, math.Sqrt(normSquaredScalar(q)), c)
					checkKernelNumber(t, "special norm", normSquared(c), wantSum, cosineTolerance*wantSum)
					checkKernelNumber(t, "special fused norm", gotSum, wantSum, cosineTolerance*wantSum)
					checkKernelNumber(t, "special dot", gotDot, wantDot, cosineTolerance*math.Abs(wantDot))
				}
			}
		})
	}
	t.Logf("max absolute cosine deviation: %.12g", maxDeviation)
}

func TestCosineNormalizedGaussian(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	var maxDeviation float64
	for _, dimensions := range []int{1536, 3072} {
		for range 512 {
			q := benchmarkUnitVector(rng, dimensions)
			c := benchmarkUnitVector(rng, dimensions)
			for _, candidate := range [][]float32{c, q} {
				dot, sum := dotNormScalar(q, candidate)
				want := dot / (math.Sqrt(normSquaredScalar(q)) * math.Sqrt(sum))
				got, err := CosineSimilarity(q, Norm(q), candidate)
				if err != nil {
					t.Fatal(err)
				}
				checkKernelNumber(t, "normalized cosine", got, want, cosineTolerance)
				maxDeviation = max(maxDeviation, math.Abs(got-want))
			}
		}
	}
	t.Logf("max absolute normalized Gaussian cosine deviation: %.12g", maxDeviation)
}

func adversarialVectors(dimensions int) [][2][]float32 {
	pairs := make([][2][]float32, 0, 8)
	for _, scale := range []float32{1, 1e-22, 1e15, 1e19, 1e30, math.SmallestNonzeroFloat32} {
		q, c := make([]float32, dimensions), make([]float32, dimensions)
		for i := range q {
			switch i % 8 {
			case 0, 1:
				q[i] = scale
				c[i] = scale
			case 2, 3:
				q[i] = scale
				c[i] = -scale
			case 4, 5:
				q[i] = scale * 1e-10
				c[i] = scale
			case 6, 7:
				q[i] = scale
				c[i] = scale * 1e-10
			}
		}
		pairs = append(pairs, [2][]float32{q, c})
	}
	// Concentrate small squares in one lane for every supported vector width.
	for _, width := range []int{4, 8, 16, 32, 64} {
		q, c := make([]float32, dimensions), make([]float32, dimensions)
		q[0] = 1
		c[0] = 1
		for i := width; i < min(dimensions, 256*width); i += width {
			c[i] = 0.00024
		}
		pairs = append(pairs, [2][]float32{q, c})
	}

	return pairs
}

func TestKernelsAdversarial(t *testing.T) {
	var maxDeviation float64
	for _, dimensions := range []int{1536, 3072} {
		for _, pair := range adversarialVectors(dimensions) {
			q, c := pair[0], pair[1]
			dot, sum := dotNormScalar(q, c)
			want := dot / (math.Sqrt(normSquaredScalar(q)) * math.Sqrt(sum))
			got, err := CosineSimilarity(q, Norm(q), c)
			if err != nil {
				t.Fatal(err)
			}
			checkKernelNumber(t, "adversarial cosine", got, want, cosineTolerance)
			maxDeviation = max(maxDeviation, math.Abs(got-want))
		}
	}
	t.Logf("max absolute adversarial cosine deviation: %.12g", maxDeviation)
}

func TestScalarKernelsPreserveOriginalOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	for _, length := range []int{0, 1, 7, 199, 768, 1536, 3072} {
		q, c := make([]float32, length), make([]float32, length)
		for i := range q {
			q[i] = float32(rng.NormFloat64())
			c[i] = float32(rng.NormFloat64())
		}
		var sum, dot float64
		for _, value := range c {
			sum += float64(value) * float64(value)
		}
		for i := range q {
			dot += float64(q[i]) * float64(c[i])
		}
		gotDot, gotSum := dotNormScalar(q, c)
		if gotDot != dot || gotSum != sum || normSquaredScalar(c) != sum {
			t.Fatalf("length %d changed scalar arithmetic", length)
		}
	}
}

func TestCosineSpecialQueries(t *testing.T) {
	for _, length := range []int{1, 7, 199, 768, 1536, 3072} {
		q, c := make([]float32, length), make([]float32, length)
		for i := range c {
			c[i] = 1
		}
		for _, value := range []float32{0, math.SmallestNonzeroFloat32, 1e-22, math.MaxFloat32, float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.NaN())} {
			for i := range q {
				q[i] = value
			}
			wantDot, wantSum := dotNormScalar(q, c)
			gotDot, gotSum := dotNorm(q, math.Sqrt(normSquaredScalar(q)), c)
			checkKernelNumber(t, "query special dot", gotDot, wantDot, cosineTolerance*math.Abs(wantDot))
			checkKernelNumber(t, "query special norm", gotSum, wantSum, cosineTolerance*wantSum)
		}
	}
}

func TestSearchExactMatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewPCG(15, 16))
	query := benchmarkUnitVector(rng, 1536)
	queryNorm := math.Sqrt(normSquaredScalar(query))
	candidates := make([]SearchCandidate[int], 64)
	expected := make([]Scored[int], len(candidates))
	for i := range candidates {
		candidate := benchmarkUnitVector(rng, 1536)
		candidates[i] = SearchCandidate[int]{Item: i, Vector: candidate}
		dot, sum := dotNormScalar(query, candidate)
		expected[i] = Scored[int]{Item: i, Score: dot / (queryNorm * math.Sqrt(sum))}
	}
	expected = TopK(expected, 20, func(a, b int) bool { return a < b })
	got, err := Search(t.Context(), query, candidates, SearchOptions[int]{Backend: BackendExact, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(expected) {
		t.Fatalf("result length %d want %d", len(got), len(expected))
	}
	for i := range got {
		if got[i].Item != expected[i].Item {
			t.Fatalf("rank %d: item %d want %d", i, got[i].Item, expected[i].Item)
		}
		checkKernelNumber(t, "search score", got[i].Score, expected[i].Score, cosineTolerance)
	}
}
