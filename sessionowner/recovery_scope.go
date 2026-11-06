package sessionowner

import (
	"context"
	"encoding/json"
	"time"
)

type RejectedDiagnosis struct {
	ID               string `json:"id"`
	Epoch            string `json:"epoch"`
	Cause            string `json:"cause"`
	Category         string `json:"category"`
	OperationID      string `json:"operationId"`
	OperationVersion int    `json:"operationVersion"`
	AdapterID        string `json:"adapterId"`
}

func payloadDigest(v any) string {
	b, e := CanonicalJSON(normalize(v))
	if e != nil {
		return ""
	}
	return Hash(b)
}
func activeClaim(c *Claim) bool { return c != nil && c.Phase != "ACCEPTED" && c.Phase != "REJECTED" }
func claimBound(s *Snapshot, c *Claim) bool {
	if c == nil || c.ID == "" || c.BindingID != s.BindingID || !validID(c.Caller) || !validID(c.OriginRequestID) || c.OriginJournalKey != c.Caller+"\x00"+c.OriginRequestID {
		return false
	}
	j := s.Journal[c.OriginJournalKey]
	return j != nil && j.RequestKind == "recover" && j.ClaimID == c.ID && j.Generation == c.Epoch && j.Caller == c.Caller
}
func recoveryConsistency(s *Snapshot) error {
	if e := validateReadCompositions(s); e != nil {
		return e
	}
	if e := validateCandidateTransfers(s); e != nil {
		return e
	}
	for id, c := range s.Claims {
		if err := validateClaimState(s, c); err != nil {
			return err
		}
		if c == nil || c.ID != id || !claimBound(s, c) {
			return fail(ReconciliationRequired, "claim_binding")
		}
		if activeClaim(c) && c.Epoch != s.Generation {
			return fail(ReconciliationRequired, "claim_epoch")
		}
		switch c.Phase {
		case "CANDIDATE_READY", "PROVING", "PROVED", "COMMITTING", "COMMIT_UNCERTAIN", "ACCEPTED":
			if c.Candidate == nil || c.CandidateExpires.IsZero() || c.CandidateDigest != payloadDigest(c.Candidate) {
				return fail(ReconciliationRequired, "candidate_binding")
			}
		}
		switch c.Phase {
		case "PROVED", "COMMITTING", "COMMIT_UNCERTAIN", "ACCEPTED":
			if c.Proof == nil || c.ProofDigest != payloadDigest(c.Proof) || c.ProofCandidateDigest != c.CandidateDigest {
				return fail(ReconciliationRequired, "proof_binding")
			}
		}
		if c.ActiveJournalKey != "" {
			j := s.Journal[c.ActiveJournalKey]
			if j == nil || j.ClaimID != id || j.Caller != c.Caller || j.Generation != c.Epoch {
				return fail(ReconciliationRequired, "effect_journal")
			}
		}
		if len(c.EffectIDs) > 0 {
			journal := s.Journal[c.ActiveJournalKey]
			if journal == nil {
				return fail(ReconciliationRequired, "effect_journal")
			}
			seen := map[string]bool{}
			for _, id := range c.EffectIDs {
				found := false
				for _, stored := range journal.EffectIDs {
					found = found || stored == id
				}
				if !found || seen[id] {
					return fail(ReconciliationRequired, "effect_ids")
				}
				seen[id] = true
			}
		}
		if c.Phase == "ACQUIRING" && len(c.EffectIDs) == 0 && c.AcquisitionAttempts != 0 {
			return fail(ReconciliationRequired, "acquisition_accounting")
		}
		if c.Phase == "PROVING" && len(c.EffectIDs) == 0 && c.ProofAttempts != 0 {
			return fail(ReconciliationRequired, "proof_accounting")
		}
		if c.Phase == "EFFECT_UNCERTAIN" && c.ActiveStep != "readiness" && c.ActiveStep != "acquire_candidate" && c.ActiveStep != "prove_candidate" {
			return fail(ReconciliationRequired, "uncertain_step")
		}
		if c.Phase == "EFFECT_UNCERTAIN" && len(c.EffectIDs) == 0 {
			return fail(ReconciliationRequired, "effect_witness")
		}
		if (c.Phase == "COMMITTING" || c.Phase == "COMMIT_UNCERTAIN" || c.Phase == "ACCEPTED") && (!validID(c.CommitOperationID) || !validID(c.ProposedGeneration)) {
			return fail(ReconciliationRequired, "commit_binding")
		}
	}
	for _, j := range s.Journal {
		if err := validateIntentReservations(s, j); err != nil {
			return err
		}
		if j == nil {
			return fail(ReconciliationRequired, "nil_journal")
		}
		if j.UnresolvedIntentID != "" && !validID(j.UnresolvedIntentID) {
			return fail(ReconciliationRequired, "intent_id")
		}
		if j.ClaimID != "" {
			if j.Dispatches < 0 || !validEffectIDs(j.EffectIDs) || len(j.EffectIDs) != j.Dispatches || (j.Certainty != "not_dispatched" && j.Certainty != "completed" && j.Certainty != "uncertain") {
				return fail(ReconciliationRequired, "claim_journal_state")
			}
			c := s.Claims[j.ClaimID]
			if !claimBound(s, c) || j.Caller != c.Caller || j.Generation != c.Epoch || j.RequestKind != "recover" {
				return fail(ReconciliationRequired, "journal_claim")
			}
		}
	}
	return nil
}
func (o *Owner) exactResume(req *Request, caller string, s *Snapshot) bool {
	if text(req.Value, "kind") != "recover" || text(req.Value, "mode") != "resume" {
		return false
	}
	c := s.Claims[text(req.Value, "claimId")]
	if !claimBound(s, c) || c.Caller != caller || c.Epoch != text(req.Value, "expectedGeneration") || c.Epoch != s.Generation {
		return false
	}
	for _, other := range s.Claims {
		if activeClaim(other) && other.ID != c.ID {
			return false
		}
	}
	for key, j := range s.Journal {
		if settledJournal(s, key) {
			continue
		}
		for _, intent := range j.Intents {
			if intent.State == "prepared" && (j.ClaimID != c.ID || j.Caller != caller || j.Generation != c.Epoch || j.RequestKind != "recover") {
				return false
			}
		}
		if ((j.Certainty == "uncertain" && j.ResolutionID == "") || j.UnresolvedIntentID != "") && (j.ClaimID != c.ID || j.Caller != caller || j.Generation != c.Epoch || j.RequestKind != "recover") {
			return false
		}
	}
	return true
}
func (o *Owner) validateRejected(req *Request, caller string, s *Snapshot) (string, error) {
	if text(req.Value, "cause") == "authorized_replacement" {
		return "", nil
	}
	j := s.Journal[caller+"\x00"+text(req.Value, "rejectedRequestId")]
	if !s.AuthBlocked || j == nil || j.Caller != caller || j.Generation != s.Generation || j.Certainty != "completed" || j.Semantics != "safe_read" || j.Diagnosis == nil {
		return "", fail(PermissionDenied, "rejected_receipt")
	}
	d := j.Diagnosis
	if d.Epoch != s.Generation || d.Cause != text(req.Value, "cause") || d.OperationID != j.OperationID || d.OperationVersion != j.OperationVersion || !validID(d.ID) {
		return "", fail(PermissionDenied, "diagnosis_binding")
	}
	op, ok := o.registry.operations[operationKey(d.OperationID, d.OperationVersion, "perform")]
	if !ok || op.Semantics != "safe_read" || op.AdapterID == nil || *op.AdapterID != d.AdapterID {
		return "", fail(PermissionDenied, "diagnosis_operation")
	}
	category := "rejected_generation"
	if d.Cause == "expired_session" {
		category = "needs_login"
	}
	if d.Category != category {
		return "", fail(PermissionDenied, "diagnosis_cause")
	}
	v, e := DecodeJSON(j.Response, HardFrameLimit)
	if e != nil {
		return "", fail(PermissionDenied, "diagnosis_response")
	}
	m, _ := object(v)
	f, _ := object(m["failure"])
	if text(m, "kind") != "failed" || text(m, "requestKind") != "perform" || text(m, "requestId") != j.RequestID || text(m, "bindingId") != s.BindingID || text(m, "acceptedGeneration") != d.Epoch || text(m, "certainty") != "completed" || text(f, "category") != category {
		return "", fail(PermissionDenied, "diagnosis_response")
	}
	return d.ID, nil
}
func (i *Invocation) consumeClaimPermit(effectID string) error {
	c := i.claim
	j := i.snapshot.Journal[i.journalKey]
	if !claimBound(i.snapshot, c) || c.Caller != j.Caller || c.Epoch != i.snapshot.Generation {
		return fail(PermissionDenied, "claim_permit_binding")
	}
	j.ClaimID = c.ID
	if i.step == "reconcile" {
		return nil
	} // Probe records are separate from the unresolved effect being reconciled.
	if c.ActiveStep != i.step || c.ActiveJournalKey != i.journalKey {
		return fail(ReconciliationRequired, "claim_step")
	}
	if len(c.EffectIDs) == 0 {
		switch i.step {
		case "acquire_candidate":
			if c.Phase != "ACQUIRING" || c.AcquisitionAttempts >= i.plan.MaxAcquisitionAttempts {
				return fail(ReconciliationRequired, "acquisition_limit")
			}
			c.AcquisitionAttempts++
		case "prove_candidate":
			if c.Phase != "PROVING" || c.CandidateDigest != payloadDigest(c.Candidate) || !c.CandidateExpires.After(i.owner.now()) || c.ProofAttempts >= i.plan.MaxProofAttempts {
				return fail(ReconciliationRequired, "proof_limit")
			}
			c.ProofAttempts++
		case "readiness":
			if c.Phase != "RESERVED" {
				return fail(ReconciliationRequired, "readiness_phase")
			}
		default:
			return fail(ProtocolMismatch, "claim_step")
		}
	}
	c.NoEffectProved = false
	c.EffectIDs = append(c.EffectIDs, effectID)
	i.snapshot.Claims[c.ID] = c
	rev, e := nextRevision(i.snapshot.ClaimVersion)
	if e != nil {
		return e
	}
	i.snapshot.ClaimVersion = rev
	return nil
}
func (o *Owner) bindStep(ctx context.Context, i *Invocation, step string) error {
	c := i.claim
	c.ActiveStep = step
	c.ActiveJournalKey = i.journalKey
	c.EffectIDs = nil
	c.NoEffectProved = true
	i.snapshot.Claims[c.ID] = c
	i.step = step
	var e error
	i.snapshot.ClaimVersion, e = nextRevision(i.snapshot.ClaimVersion)
	if e != nil {
		return e
	}
	i.snapshot, e = o.save(ctx, i.guard, i.snapshot)
	if e == nil {
		i.claim = i.snapshot.Claims[c.ID]
	}
	return e
}
func resolveClaimJournals(s *Snapshot, c *Claim, evidence string) {
	for _, j := range s.Journal {
		if j.ClaimID == c.ID && j.Caller == c.Caller && j.Generation == c.Epoch && j.RequestKind == "recover" {
			j.ResolutionID = evidence
		}
	}
}
func recoveryCertainty(i *Invocation) string {
	j := i.snapshot.Journal[i.journalKey]
	if j.Certainty == "uncertain" && j.ResolutionID == "" {
		return "uncertain"
	}
	if j.Dispatches > 0 {
		return "completed"
	}
	return "not_dispatched"
}
func (o *Owner) recoveryEligible(i *Invocation) bool {
	now := o.now()
	if now.Before(i.claim.RetryNotBefore) {
		return false
	}
	for _, t := range i.snapshot.Fences {
		if now.Before(t) {
			return false
		}
	}
	return true
}

type RecoveryReconcileRequest struct {
	BindingID, ClaimID, Caller, Epoch, RequestID, Step, CandidateDigest string
	EffectIDs                                                           []string
}
type RecoveryReconcileResult struct {
	CandidateTransferID string
	Request             RecoveryReconcileRequest
	Status              string // candidate_ready, proved, no_effect, rejected, uncertain
	EvidenceID          string
	Candidate, Proof    map[string]any
	CandidateExpires    time.Time
}

// Implemented only by the pinned provider adapter. It is not a public operation,
// acquisition fallback or another action manager. Effects still require BeforeEffect.
type RecoveryReconciler interface {
	ReconcileRecovery(context.Context, *Invocation, RecoveryReconcileRequest) (RecoveryReconcileResult, error)
}

func (o *Owner) acceptedReadback(ctx context.Context, g *Guard, req *Request, caller string, s *Snapshot, c *Claim) ([]byte, error) {
	if !claimBound(s, c) || c.Caller != caller || c.Epoch != text(req.Value, "expectedGeneration") || c.Phase != "ACCEPTED" || len(c.AcceptedOutcome) == 0 {
		return o.failed(req, nil, nil, PermissionDenied, "not_dispatched", newID())
	}
	r, e := o.backend.Reconcile(ctx, g, c.CommitOperationID)
	stored, ok := s.Commits[c.CommitOperationID]
	if e != nil || !ok || !receiptMatches(r, stored) || r.Generation != c.ProposedGeneration {
		return o.failed(req, nil, c, ReconciliationRequired, "uncertain", newID())
	}
	v, e := DecodeJSON(c.AcceptedOutcome, HardFrameLimit)
	if e != nil {
		return o.failed(req, nil, c, StorageFailure, "uncertain", newID())
	}
	m, _ := object(v)
	if text(m, "finalGeneration") != c.ProposedGeneration || text(m, "acceptedGeneration") != c.Epoch || text(m, "kind") != "recovered" {
		return o.failed(req, nil, c, StorageFailure, "uncertain", newID())
	}
	status, _ := object(m["claim"])
	journal := s.Journal[c.Caller+"\x00"+c.CommitRequestID]
	if text(status, "claimId") != c.ID || text(m, "bindingId") != s.BindingID || r.StateRevision != c.CommitBaseRevision+1 || journal == nil || journal.ClaimID != c.ID || !equalJSON(normalize(json.RawMessage(journal.Response)), normalize(json.RawMessage(c.AcceptedOutcome))) {
		return o.failed(req, nil, c, ReconciliationRequired, "uncertain", newID())
	}
	m["requestId"] = text(req.Value, "requestId")
	req.acceptedGeneration = c.Epoch
	b, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	return b, o.registry.ValidateResponse(b, req)
}
