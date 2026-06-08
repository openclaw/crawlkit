package vector

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

type InvalidVectorPolicy string

const (
	InvalidVectorError InvalidVectorPolicy = "error"
	InvalidVectorSkip  InvalidVectorPolicy = "skip"
)

type SearchCandidate[T any] struct {
	Item   T
	Vector []float32
}

type SearchResult[T any] struct {
	Item  T
	Score float64
}

type SearchOptions[T any] struct {
	Backend       string
	Limit         int
	MinScore      *float64
	Exclude       func(T) bool
	TieLess       func(left, right T) bool
	InvalidVector InvalidVectorPolicy
	TurboVec      TurboVecOptions
}

func Search[T any](ctx context.Context, query []float32, candidates []SearchCandidate[T], opts SearchOptions[T]) ([]SearchResult[T], error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" {
		backend = BackendExact
	}
	filtered := filterCandidates(candidates, opts.Exclude)
	switch backend {
	case BackendExact:
		return exactSearch(query, filtered, opts)
	case BackendTurboVec:
		return turboVecSearch(ctx, query, filtered, opts)
	default:
		return nil, fmt.Errorf("unsupported vector backend %q", opts.Backend)
	}
}

func filterCandidates[T any](candidates []SearchCandidate[T], exclude func(T) bool) []SearchCandidate[T] {
	if exclude == nil {
		return candidates
	}
	out := make([]SearchCandidate[T], 0, len(candidates))
	for _, candidate := range candidates {
		if exclude(candidate.Item) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func exactSearch[T any](query []float32, candidates []SearchCandidate[T], opts SearchOptions[T]) ([]SearchResult[T], error) {
	queryNorm := Norm(query)
	if queryNorm == 0 {
		return nil, errors.New("query vector is zero")
	}
	scored := make([]Scored[SearchResult[T]], 0, len(candidates))
	for _, candidate := range candidates {
		score, err := CosineSimilarity(query, queryNorm, candidate.Vector)
		if err != nil {
			if opts.InvalidVector == InvalidVectorSkip {
				continue
			}
			return nil, err
		}
		if !validScore(score, opts.MinScore) {
			continue
		}
		result := SearchResult[T]{Item: candidate.Item, Score: score}
		scored = append(scored, Scored[SearchResult[T]]{Item: result, Score: score})
	}
	top := TopK(scored, opts.Limit, func(left, right SearchResult[T]) bool {
		if opts.TieLess == nil {
			return false
		}
		return opts.TieLess(left.Item, right.Item)
	})
	out := make([]SearchResult[T], len(top))
	for i, item := range top {
		out[i] = item.Item
	}
	return out, nil
}

func validScore(score float64, minScore *float64) bool {
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return false
	}
	return minScore == nil || score >= *minScore
}
