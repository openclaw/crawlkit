package sessionowner

import (
	"encoding/json"
	"time"
)

// Reconciliation proves provenance, not eligibility. Both ordinary acquisition
// and reconciled candidate_ready use these same private admission fences.
// It is deliberately pure: a refusal does not clear auth stops, discard retained
// candidate/proof evidence, alter expiry, or normalize a rejected fingerprint.
func (o *Owner) checkCandidateAdmission(i *Invocation, candidate map[string]any, expires time.Time) error {
	if i.claim == nil || i.request.Operation.AdapterID == nil || !claimBound(i.snapshot, i.claim) || i.claim.Epoch != i.snapshot.Generation {
		return fail(ReconciliationRequired, "candidate_scope")
	}
	binding, ok := o.registry.adapters[*i.request.Operation.AdapterID]
	if !ok || binding.CandidateSchemaID == nil {
		return fail(ProtocolMismatch, "candidate_schema")
	}
	raw, err := json.Marshal(candidate)
	if err != nil || candidate == nil || CheckBytes(raw, i.request.Operation.WireCaps.MaxCandidateBytes) != nil || o.registry.schemas.Validate(*binding.CandidateSchemaID, normalize(candidate)) != nil {
		return fail(Code("rejected_generation"), "candidate_shape")
	}
	if expires.IsZero() || !expires.After(o.now()) {
		return fail(Code("rejected_generation"), "candidate_expired")
	}
	digest := payloadDigest(candidate)
	if i.claim.Candidate != nil && (digest != i.claim.CandidateDigest || expires.After(i.claim.CandidateExpires)) {
		return fail(ReconciliationRequired, "candidate_changed")
	}
	if i.claim.Proof != nil && (i.claim.ProofCandidateDigest != digest || i.claim.ProofDigest != payloadDigest(i.claim.Proof)) {
		return fail(ReconciliationRequired, "candidate_proof_binding")
	}
	if i.snapshot.AuthBlocked && digest == payloadDigest(i.snapshot.Credentials) {
		return fail(Code("rejected_generation"), "current_rejected_candidate")
	}
	for _, prior := range i.snapshot.Claims {
		if prior != nil && prior.ID != i.claim.ID && prior.Phase == "REJECTED" && prior.CandidateDigest == digest {
			return fail(Code("rejected_generation"), "historical_rejected_candidate")
		}
	}
	return nil
}
