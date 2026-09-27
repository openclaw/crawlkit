//go:build goexperiment.simd

package vector

import (
	"math"
	"simd"
)

// Bound each lane to 16 products regardless of the hardware vector width.
const simdBlockIters = 16

// Reserve stack space for up to 2048-bit SVE vectors.
const simdMaxLanes = 64

// Emulation is slower than the scalar loops.
var simdEmulated = simd.Emulated()

func normSquared(values []float32) float64 {
	if simdEmulated {
		return normSquaredScalar(values)
	}
	return normSquaredSIMD(values, simdBlockIters)
}

func normSquaredSIMD(values []float32, blockIters int) float64 {
	var sum float64
	var lanes [simdMaxLanes]float32
	width := (simd.Float32s{}).Len()
	i := 0
	for len(values)-i >= width {
		end := i + min(blockIters, (len(values)-i)/width)*width
		var partial simd.Float32s
		for ; i < end; i += width {
			v := simd.LoadFloat32s(values[i:])
			partial = v.MulAdd(v, partial)
		}
		// Flush bounded lane sums into float64 before rounding error grows.
		partial.Store(lanes[:])
		for _, value := range lanes[:width] {
			sum += float64(value)
		}
	}
	for _, value := range values[i:] {
		sum += float64(value) * float64(value)
	}
	// Float64 preserves the scalar range for tiny, huge, and non-finite inputs.
	if sum < 1e-30 || math.IsInf(sum, 0) || math.IsNaN(sum) {
		return normSquaredScalar(values)
	}
	return sum
}

func dotNorm(query []float32, queryNorm float64, candidate []float32) (float64, float64) {
	if simdEmulated {
		return dotNormScalar(query, candidate)
	}
	return dotNormSIMD(query, queryNorm, candidate, simdBlockIters)
}

func dotNormSIMD(query []float32, queryNorm float64, candidate []float32, blockIters int) (dot, sum float64) {
	if !(queryNorm >= 1e-15 && queryNorm <= 1e15) {
		return dotNormScalar(query, candidate)
	}
	var dots, sums [simdMaxLanes]float32
	width := (simd.Float32s{}).Len()
	i := 0
	for len(candidate)-i >= width {
		end := i + min(blockIters, (len(candidate)-i)/width)*width
		var dotPartial, normPartial simd.Float32s
		for ; i < end; i += width {
			q := simd.LoadFloat32s(query[i:])
			v := simd.LoadFloat32s(candidate[i:])
			dotPartial = q.MulAdd(v, dotPartial)
			normPartial = v.MulAdd(v, normPartial)
		}
		dotPartial.Store(dots[:])
		normPartial.Store(sums[:])
		for lane := range width {
			dot += float64(dots[lane])
			sum += float64(sums[lane])
		}
	}
	for ; i < len(candidate); i++ {
		q, v := float64(query[i]), float64(candidate[i])
		dot += q * v
		sum += v * v
	}
	// The norm product bounds each product; keep float32 range risks scalar.
	normProduct := queryNorm * math.Sqrt(sum)
	if sum < 1e-30 || !(normProduct >= 1e-30 && normProduct <= 1e30) || math.IsInf(dot, 0) || math.IsNaN(dot) {
		return dotNormScalar(query, candidate)
	}
	return dot, sum
}
