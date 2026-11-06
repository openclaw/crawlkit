package sessionowner

import (
	"bytes"
	"strconv"
)

// Persisted phases are a closed set. Unknown data is neither inactive nor a
// terminal claim, and is never normalized into a supported phase.
func supportedClaimPhase(p string) bool {
	switch p {
	case "RESERVED", "WAIT_READINESS", "ACQUIRING", "CANDIDATE_READY", "PROVING", "PROVED", "COMMITTING", "REJECTED", "EFFECT_UNCERTAIN", "COMMIT_UNCERTAIN", "ACCEPTED", "CANCELLED_BEFORE_EFFECT":
		return true
	default:
		return false
	}
}
func validEffectIDs(ids []string) bool {
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func emptyCandidate(c *Claim) bool {
	return c.Candidate == nil && c.CandidateDigest == "" && c.CandidateExpires.IsZero() && c.Proof == nil && c.ProofDigest == "" && c.ProofCandidateDigest == ""
}
func validCandidate(c *Claim) bool {
	return c.Candidate != nil && c.AcquisitionAttempts == 1 && !c.CandidateExpires.IsZero() && c.CandidateDigest == payloadDigest(c.Candidate)
}
func validProof(c *Claim) bool {
	return validCandidate(c) && c.Proof != nil && c.ProofAttempts == 1 && c.ProofDigest == payloadDigest(c.Proof) && c.ProofCandidateDigest == c.CandidateDigest
}

func validateClaimState(s *Snapshot, c *Claim) error {
	bad := func() error { return fail(ReconciliationRequired, "claim_state") }
	if c == nil || !supportedClaimPhase(c.Phase) || !validID(c.Epoch) || !validID(c.ID) {
		return bad()
	}
	if c.Cause != "rejected_generation" && c.Cause != "expired_session" && c.Cause != "authorized_replacement" {
		return bad()
	}
	if c.ReadinessResumptions < 0 || c.ReadinessResumptions > 2 || c.AcquisitionAttempts < 0 || c.AcquisitionAttempts > 1 || c.ProofAttempts < 0 || c.ProofAttempts > 1 || c.CommitAttempts < 0 || c.CommitAttempts > 3 {
		return bad()
	}
	if !validEffectIDs(c.EffectIDs) || (c.ActiveStep == "") != (c.ActiveJournalKey == "") {
		return bad()
	}
	if c.ActiveStep != "" && c.ActiveStep != "readiness" && c.ActiveStep != "acquire_candidate" && c.ActiveStep != "prove_candidate" {
		return bad()
	}
	if len(c.EffectIDs) > 0 && (c.ActiveStep == "" || c.NoEffectProved) {
		return bad()
	}
	if c.Candidate == nil {
		if !emptyCandidate(c) || c.ProofAttempts != 0 {
			return bad()
		}
	} else if !validCandidate(c) {
		return bad()
	}
	if c.Proof == nil {
		if c.ProofDigest != "" || c.ProofCandidateDigest != "" {
			return bad()
		}
	} else if !validProof(c) {
		return bad()
	}
	if c.AcquisitionAttempts == 0 && (c.Candidate != nil || c.ProofAttempts != 0) {
		return bad()
	}
	if c.ActiveStep == "acquire_candidate" && len(c.EffectIDs) > 0 && c.AcquisitionAttempts != 1 {
		return bad()
	}
	if c.ActiveStep == "prove_candidate" && len(c.EffectIDs) > 0 && c.ProofAttempts != 1 {
		return bad()
	}
	if c.CommitAttempts == 0 {
		if c.CommitOperationID != "" || c.ProposedGeneration != "" || c.CommitRequestID != "" || c.CommitBaseRevision != 0 || len(c.PriorCommitIDs) != 0 || len(c.AcceptedOutcome) != 0 {
			return bad()
		}
	} else {
		if !validID(c.CommitOperationID) || !validID(c.ProposedGeneration) || c.ProposedGeneration == c.Epoch || !validID(c.CommitRequestID) || c.CommitBaseRevision == 0 || c.CommitBaseRevision > s.StateRevision || len(c.PriorCommitIDs) != c.CommitAttempts-1 || !validEffectIDs(c.PriorCommitIDs) {
			return bad()
		}
		for _, id := range c.PriorCommitIDs {
			if id == c.CommitOperationID {
				return bad()
			}
		}
		j := s.Journal[c.Caller+"\x00"+c.CommitRequestID]
		if j == nil || j.ClaimID != c.ID || j.Caller != c.Caller || j.Generation != c.Epoch || j.RequestKind != "recover" {
			return bad()
		}
		if c.Phase != "ACCEPTED" {
			if _, known := s.Commits[c.CommitOperationID]; known {
				return bad()
			}
		}
		switch c.Phase {
		case "PROVED", "COMMITTING", "COMMIT_UNCERTAIN", "ACCEPTED", "REJECTED":
		default:
			return bad()
		}
	}
	if c.Phase != "ACCEPTED" && len(c.AcceptedOutcome) != 0 {
		return bad()
	}
	switch c.Phase {
	case "RESERVED":
		if !emptyCandidate(c) || c.AcquisitionAttempts != 0 || c.ActiveStep != "" && c.ActiveStep != "readiness" {
			return bad()
		}
	case "WAIT_READINESS", "CANCELLED_BEFORE_EFFECT":
		if !emptyCandidate(c) || c.AcquisitionAttempts != 0 || !c.NoEffectProved || len(c.EffectIDs) != 0 || c.ActiveStep == "prove_candidate" {
			return bad()
		}
	case "ACQUIRING":
		if !emptyCandidate(c) || c.ProofAttempts != 0 || c.ActiveStep != "" && c.ActiveStep != "acquire_candidate" || (c.AcquisitionAttempts == 1) != (len(c.EffectIDs) > 0) {
			return bad()
		}
	case "CANDIDATE_READY":
		if !validCandidate(c) || c.ProofAttempts != 0 || c.Proof != nil || c.ActiveStep == "readiness" || c.ActiveStep == "prove_candidate" && len(c.EffectIDs) > 0 {
			return bad()
		}
	case "PROVING":
		if !validCandidate(c) || c.Proof != nil || c.ActiveStep == "readiness" || c.ProofAttempts == 1 && (c.ActiveStep != "prove_candidate" || len(c.EffectIDs) == 0) {
			return bad()
		}
	case "PROVED":
		if !validProof(c) || c.ActiveStep != "" && c.ActiveStep != "prove_candidate" {
			return bad()
		}
	case "COMMITTING", "COMMIT_UNCERTAIN":
		if !validProof(c) || c.CommitAttempts == 0 || c.ActiveStep != "" && c.ActiveStep != "prove_candidate" {
			return bad()
		}
		if _, known := s.Commits[c.CommitOperationID]; known {
			return bad()
		}
	case "EFFECT_UNCERTAIN":
		if len(c.EffectIDs) == 0 || c.NoEffectProved || c.CommitAttempts != 0 {
			return bad()
		}
		switch c.ActiveStep {
		case "readiness":
			if !emptyCandidate(c) || c.AcquisitionAttempts != 0 {
				return bad()
			}
		case "acquire_candidate":
			if !emptyCandidate(c) || c.AcquisitionAttempts != 1 {
				return bad()
			}
		case "prove_candidate":
			if !validCandidate(c) || c.ProofAttempts != 1 || c.Proof != nil {
				return bad()
			}
		default:
			return bad()
		}
	case "REJECTED": // Retained evidence/counts are not refunded or cleared.
	case "ACCEPTED":
		if !validProof(c) || c.CommitAttempts == 0 || len(c.AcceptedOutcome) == 0 {
			return bad()
		}
		r, ok := s.Commits[c.CommitOperationID]
		if !ok || r.Certainty != "committed" || r.OperationID != c.CommitOperationID || r.Generation != c.ProposedGeneration || r.StateRevision != c.CommitBaseRevision+1 || r.StateRevision > s.StateRevision || len(r.Digest) != 64 {
			return bad()
		}
		v, e := DecodeJSON(c.AcceptedOutcome, HardFrameLimit)
		if e != nil {
			return bad()
		}
		m, ok := object(v)
		if !ok {
			return bad()
		}
		status, _ := object(m["claim"])
		j := s.Journal[c.Caller+"\x00"+c.CommitRequestID]
		if text(m, "protocol") != Protocol || text(m, "kind") != "recovered" || text(m, "bindingId") != c.BindingID || text(m, "acceptedGeneration") != c.Epoch || text(m, "finalGeneration") != c.ProposedGeneration || text(m, "stateRevision") != strconv.FormatUint(r.StateRevision, 10) || text(status, "claimId") != c.ID || text(status, "phase") != "ACCEPTED" || j == nil || !bytes.Equal(j.Response, c.AcceptedOutcome) {
			return bad()
		}
	}
	return nil
}
