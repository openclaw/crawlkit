package sessionowner

import (
	"context"
	"time"
)

// ProtectedCandidateFiles is immutable staging, not active credential authority.
// Its directory and binding are fixed trusted owner configuration. Public callers
// receive no path selector; ref is a random opaque name inside that private root.
type ProtectedCandidateFiles struct{ directory, binding string }
type candidateFileHeader struct {
	Version   string         `json:"version"`
	Scope     CandidateScope `json:"scope"`
	ExpiresAt time.Time      `json:"expiresAt"`
	Bytes     int            `json:"bytes"`
	Digest    string         `json:"digest"`
}

const candidateFileMagic = "SESSIONOWNER-CANDIDATE-1\n"
const candidateHeaderCap = 16384

func opaqueCandidateRef(ref string) bool {
	if len(ref) != 32 {
		return false
	}
	for _, c := range ref {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ProtectedProducerExpiry is an explicit qualifier for trusted protected-file
// producer metadata. Inline input has no such metadata and is refused; a provider
// needing inline transfer must supply its own qualified expiry callback.
func ProtectedProducerExpiry(ctx context.Context, scope CandidateScope, value map[string]any, material CandidateMaterial) (time.Time, error) {
	if ctx.Err() != nil || !candidateScopeEqual(scope, material.Scope) || material.ExpiresAt.IsZero() {
		return time.Time{}, fail(ProtocolMismatch, "producer_expiry")
	}
	return material.ExpiresAt, nil
}

// Check decoded names before encoding/json can apply case-insensitive struct
// matching. The spelling is the existing canonical Write format, not a new ABI.
func exactCandidateMetadata(value any) bool {
	header, ok := object(value)
	if !ok || !exactCandidateKeys(header, []string{"version", "scope", "expiresAt", "bytes", "digest"}) {
		return false
	}
	scope, ok := object(header["scope"])
	return ok && exactCandidateKeys(scope, []string{"BindingID", "Caller", "RequestID", "OperationID", "OperationDigest", "OperationVersion", "AdapterID", "AdapterArtifact", "ClaimID", "Epoch", "LeaseID", "InvocationID", "ParentLifetimeID", "Step", "Direction", "LeaseDeadline"})
}
func exactCandidateKeys(value map[string]any, keys []string) bool {
	if len(value) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}
