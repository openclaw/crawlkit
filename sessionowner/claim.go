package sessionowner

import "time"

type Claim struct {
	CandidateTransferKey string   `json:"candidateTransferKey"`
	CommitAttempts       int      `json:"commitAttempts"`
	PriorCommitIDs       []string `json:"priorCommitIds"`
	CommitRequestID      string   `json:"commitRequestId"`
	BindingID            string   `json:"bindingId"`
	Caller               string   `json:"caller"`
	OriginRequestID      string   `json:"originRequestId"`
	OriginJournalKey     string   `json:"originJournalKey"`
	ActiveJournalKey     string   `json:"activeJournalKey"`
	ActiveStep           string   `json:"activeStep"`
	EffectIDs            []string `json:"effectIds"`
	CandidateDigest      string   `json:"candidateDigest"`
	ProofDigest          string   `json:"proofDigest"`
	ProofCandidateDigest string   `json:"proofCandidateDigest"`
	CommitOperationID    string   `json:"commitOperationId"`
	ProposedGeneration   string   `json:"proposedGeneration"`
	CommitBaseRevision   uint64   `json:"commitBaseRevision,string"`
	AcceptedOutcome      []byte   `json:"acceptedOutcome"`
	DiagnosisID          string   `json:"diagnosisId"`

	ID                   string         `json:"id"`
	Epoch                string         `json:"epoch"`
	Cause                string         `json:"cause"`
	Phase                string         `json:"phase"`
	ReadinessResumptions int            `json:"readinessResumptions"`
	AcquisitionAttempts  int            `json:"acquisitionAttempts"`
	ProofAttempts        int            `json:"proofAttempts"`
	NoEffectProved       bool           `json:"noEffectProved"`
	Candidate            map[string]any `json:"candidate"`
	Proof                map[string]any `json:"proof"`
	CandidateExpires     time.Time      `json:"candidateExpires"`
	RetryNotBefore       time.Time      `json:"retryNotBefore"`
	LastEvidenceID       string         `json:"lastEvidenceId"`
}

var claimEdges = map[string]map[string]bool{
	"RESERVED":       {"WAIT_READINESS": true, "ACQUIRING": true, "CANCELLED_BEFORE_EFFECT": true, "EFFECT_UNCERTAIN": true},
	"WAIT_READINESS": {"RESERVED": true}, "CANCELLED_BEFORE_EFFECT": {"RESERVED": true},
	"ACQUIRING":        {"CANDIDATE_READY": true, "REJECTED": true, "EFFECT_UNCERTAIN": true},
	"CANDIDATE_READY":  {"PROVING": true, "REJECTED": true},
	"PROVING":          {"PROVED": true, "REJECTED": true, "EFFECT_UNCERTAIN": true},
	"PROVED":           {"COMMITTING": true, "REJECTED": true},
	"COMMITTING":       {"ACCEPTED": true, "COMMIT_UNCERTAIN": true},
	"COMMIT_UNCERTAIN": {"ACCEPTED": true, "PROVED": true},
	"EFFECT_UNCERTAIN": {"CANDIDATE_READY": true, "PROVED": true, "REJECTED": true},
}

type ClaimEvidence struct {
	ID               string
	NoEffect         bool
	Reconciled       bool
	Candidate        map[string]any
	Proof            map[string]any
	CandidateExpires time.Time
}

// AdvanceClaim is pure. Storage publishes its result and counters atomically.
// Evidence comes from a qualified trusted adapter, never public request fields.
func AdvanceClaim(c Claim, next string, e ClaimEvidence, p BudgetPlan, now time.Time) (Claim, error) {
	if !claimEdges[c.Phase][next] || !validID(e.ID) {
		return c, fail(ProtocolMismatch, "claim_transition")
	}
	if (c.Phase == "EFFECT_UNCERTAIN" || c.Phase == "COMMIT_UNCERTAIN") && !e.Reconciled {
		return c, fail(ReconciliationRequired, "claim_uncertain")
	}
	if next == "RESERVED" {
		if !c.NoEffectProved || !e.NoEffect || now.Before(c.RetryNotBefore) || c.ReadinessResumptions >= p.MaxReadinessResumptions {
			return c, fail(ReconciliationRequired, "claim_resume")
		}
		c.ReadinessResumptions++
	}
	if next == "WAIT_READINESS" || next == "CANCELLED_BEFORE_EFFECT" {
		if !e.NoEffect || c.AcquisitionAttempts != 0 {
			return c, fail(ReconciliationRequired, "no_effect")
		}
		c.NoEffectProved = true
	}
	if next == "ACQUIRING" {
		if c.AcquisitionAttempts >= p.MaxAcquisitionAttempts || now.Before(c.RetryNotBefore) {
			return c, fail(ReconciliationRequired, "acquisition_fence")
		}
		c.NoEffectProved = false
	}
	if next == "CANDIDATE_READY" {
		if e.Candidate == nil || e.CandidateExpires.IsZero() || !e.CandidateExpires.After(now) {
			return c, fail(Code("rejected_generation"), "candidate")
		}
		c.Candidate = cloneMap(e.Candidate)
		c.CandidateDigest = payloadDigest(c.Candidate)
		c.CandidateExpires = e.CandidateExpires
	}
	if next == "PROVING" {
		if c.Candidate == nil || !c.CandidateExpires.After(now) || c.ProofAttempts >= p.MaxProofAttempts {
			return c, fail(Code("rejected_generation"), "proof_fence")
		}
	}
	if next == "PROVED" {
		if e.Proof == nil || c.Candidate == nil || !c.CandidateExpires.After(now) {
			return c, fail(Code("wrong_account"), "proof")
		}
		c.Proof = cloneMap(e.Proof)
		c.ProofDigest = payloadDigest(c.Proof)
		c.ProofCandidateDigest = c.CandidateDigest
	}
	if next == "COMMITTING" && (c.Proof == nil || !c.CandidateExpires.After(now)) {
		return c, fail(Code("rejected_generation"), "proof_expired")
	}
	c.Phase = next
	c.LastEvidenceID = e.ID
	return c, nil
}

// ClaimForGeneration returns the existing claim rather than multiplying attempts.
func ClaimForGeneration(s *Snapshot, cause string, now time.Time, minRetry time.Duration) (*Claim, bool, error) {
	if s.Generation == "" {
		return nil, false, fail(Code("enrollment_required"), "enrollment")
	}
	for _, c := range s.Claims {
		if c.Epoch == s.Generation && c.Cause == cause {
			return c, false, nil
		}
	}
	if len(s.Claims) >= 1024 {
		return nil, false, fail(StorageFailure, "claim_capacity")
	}
	c := &Claim{ID: newID(), Epoch: s.Generation, Cause: cause, Phase: "RESERVED", RetryNotBefore: now}
	// A reservation applies to subsequent claims/resumptions, not the acquisition
	// in the operation that just reserved it. The owner persists the external fence.
	s.Claims[c.ID] = c
	s.Fences["recovery"] = now.Add(minRetry)
	return c, true, nil
}
