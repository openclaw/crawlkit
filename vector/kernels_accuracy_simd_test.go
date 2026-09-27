//go:build goexperiment.simd

package vector

import (
	"fmt"
	"math"
	"math/rand/v2"
	"simd"
	"testing"
)

func TestSIMDBlockAccuracy(t *testing.T) {
	t.Logf("emulated=%v vector bits=%d", simd.Emulated(), simd.VectorBitSize())
	for _, block := range []int{16, 32, 64, 128, 256} {
		rng := rand.New(rand.NewPCG(9, 10))
		var maxDeviation float64
		for _, dimensions := range []int{1536, 3072} {
			for _, pair := range adversarialVectors(dimensions) {
				q, c := pair[0], pair[1]
				dot, sum := dotNormSIMD(q, math.Sqrt(normSquaredSIMD(q, block)), c, block)
				wantDot, wantSum := dotNormScalar(q, c)
				got := dot / (math.Sqrt(normSquaredSIMD(q, block)) * math.Sqrt(sum))
				want := wantDot / (math.Sqrt(normSquaredScalar(q)) * math.Sqrt(wantSum))
				maxDeviation = max(maxDeviation, math.Abs(got-want))
			}
			for range 512 {
				q := benchmarkUnitVector(rng, dimensions)
				c := benchmarkUnitVector(rng, dimensions)
				for _, scale := range []float32{1, 1e-22, 1e15, 1e19, 1e30} {
					for _, same := range []bool{false, true} {
						qs, cs := make([]float32, dimensions), make([]float32, dimensions)
						for i := range qs {
							qs[i] = q[i] * scale
							if same {
								cs[i] = qs[i]
							} else {
								cs[i] = c[i] * scale
							}
						}
						dot, sum := dotNormSIMD(qs, math.Sqrt(normSquaredSIMD(qs, block)), cs, block)
						wantDot, wantSum := dotNormScalar(qs, cs)
						got := dot / (math.Sqrt(normSquaredSIMD(qs, block)) * math.Sqrt(sum))
						want := wantDot / (math.Sqrt(normSquaredScalar(qs)) * math.Sqrt(wantSum))
						checkKernelNumber(t, "block cosine", got, want, cosineTolerance)
						maxDeviation = max(maxDeviation, math.Abs(got-want))
					}
				}
			}
		}
		t.Logf("iterations=%d max absolute cosine deviation=%.12g", block, maxDeviation)
		if block == simdBlockIters && maxDeviation > cosineTolerance {
			t.Fatalf("selected block exceeds tolerance: %g", maxDeviation)
		}
	}
}

func BenchmarkSIMDBlock(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 2))
	q := benchmarkUnitVector(rng, 1536)
	c := benchmarkUnitVector(rng, 1536)
	queryNorm := Norm(q)
	for _, block := range []int{16, 32, 64, 128, 256} {
		b.Run(fmt.Sprint(block), func(b *testing.B) {
			for b.Loop() {
				dot, sum := dotNormSIMD(q, queryNorm, c, block)
				benchmarkScore = dot + sum
			}
		})
	}
}

func TestSIMDEmulatedFallback(t *testing.T) {
	if !simd.Emulated() {
		t.Skip("hardware SIMD active")
	}
	rng := rand.New(rand.NewPCG(13, 14))
	q := benchmarkUnitVector(rng, 1536)
	c := benchmarkUnitVector(rng, 1536)
	wantDot, wantSum := dotNormScalar(q, c)
	gotDot, gotSum := dotNorm(q, math.Sqrt(normSquaredScalar(q)), c)
	if !simdEmulated || wantDot != gotDot || wantSum != gotSum || normSquared(c) != wantSum {
		t.Fatal("emulation must dispatch to scalar")
	}
}

func TestSIMDRangeFallback(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 18))
	for _, scales := range [][2]float32{
		{1e-30, 1}, {1e30, 1}, {1, 1e-30}, {1, 1e30}, {1e14, 1e18},
		{float32(math.NaN()), 1}, {float32(math.Inf(1)), 1},
		{1, float32(math.NaN())}, {1, float32(math.Inf(1))},
	} {
		q, c := benchmarkUnitVector(rng, 1536), benchmarkUnitVector(rng, 1536)
		for i := range q {
			q[i] *= scales[0]
			c[i] *= scales[1]
		}
		queryNorm := math.Sqrt(normSquaredScalar(q))
		wantDot, wantSum := dotNormScalar(q, c)
		gotDot, gotSum := dotNormSIMD(q, queryNorm, c, simdBlockIters)
		checkKernelNumber(t, "range fallback dot", gotDot, wantDot, 0)
		checkKernelNumber(t, "range fallback norm", gotSum, wantSum, 0)
	}
}
