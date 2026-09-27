package vector

func normSquaredScalar(values []float32) float64 {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	return sum
}

func dotNormScalar(query, candidate []float32) (dot, sum float64) {
	for i, value := range candidate {
		v := float64(value)
		sum += v * v
		dot += float64(query[i]) * v
	}
	return dot, sum
}
