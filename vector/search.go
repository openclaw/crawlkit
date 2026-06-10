package vector

import (
	"context"
	"fmt"
	"math"
	"sort"
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
	Backend       SearchBackend
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
	backend := SearchBackend(strings.ToLower(strings.TrimSpace(string(opts.Backend))))
	if backend == "" {
		backend = BackendExact
	}
	if err := validateSearchVector(query, len(query), "query", true); err != nil {
		return nil, err
	}
	filtered := filterCandidates(candidates, opts.Exclude)
	switch backend {
	case BackendExact:
		return exactSearch(ctx, query, filtered, opts)
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

func exactSearch[T any](ctx context.Context, query []float32, candidates []SearchCandidate[T], opts SearchOptions[T]) ([]SearchResult[T], error) {
	queryNorm := Norm(query)
	scored := make([]Scored[SearchResult[T]], 0, len(candidates))
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateSearchVector(candidate.Vector, len(query), "candidate", true); err != nil {
			if opts.InvalidVector == InvalidVectorSkip {
				continue
			}
			return nil, err
		}
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
	if err := ctx.Err(); err != nil {
		return nil, err
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

func validateSearchVector(values []float32, dimensions int, name string, requireNonZero bool) error {
	if len(values) == 0 {
		return fmt.Errorf("%s vector is empty", name)
	}
	if err := ValidateDimensions(values, dimensions); err != nil {
		return err
	}
	var sum float64
	for i, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("%s vector contains non-finite value at index %d", name, i)
		}
		sum += float64(value) * float64(value)
	}
	if requireNonZero && sum == 0 {
		return fmt.Errorf("%s vector is zero", name)
	}
	return nil
}

func validScore(score float64, minScore *float64) bool {
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return false
	}
	return minScore == nil || score >= *minScore
}

func sortSearchResults[T any](results []SearchResult[T], tieLess func(left, right T) bool) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if tieLess == nil {
			return false
		}
		return tieLess(results[i].Item, results[j].Item)
	})
}
