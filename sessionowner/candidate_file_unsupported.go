//go:build !darwin

package sessionowner

import (
	"context"
	"time"
)

func NewProtectedCandidateFiles(string, string) (*ProtectedCandidateFiles, error) {
	return nil, fail(UnsafeStorage, "candidate_platform_unqualified")
}
func (*ProtectedCandidateFiles) Read(context.Context, CandidateScope, string, int) (CandidateMaterial, error) {
	return CandidateMaterial{}, fail(UnsafeStorage, "candidate_platform_unqualified")
}
func (*ProtectedCandidateFiles) Write(context.Context, CandidateScope, []byte, time.Time, int) (CandidateReference, error) {
	return CandidateReference{}, fail(UnsafeStorage, "candidate_platform_unqualified")
}
