//go:build !goexperiment.simd

package vector

func normSquared(values []float32) float64 { return normSquaredScalar(values) }

func dotNorm(query []float32, queryNorm float64, candidate []float32) (float64, float64) {
	return dotNormScalar(query, candidate)
}
