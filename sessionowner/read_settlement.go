package sessionowner

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const readSettlementBudget = 2 * time.Second
const readSettlementQueueBudget = 250 * time.Millisecond

// ReadSettlementDescriptor contains only operation/authority metadata, never a
// Snapshot, credentials, candidate bytes or private credential fingerprints.
type ReadSettlementDescriptor struct {
	Action, BindingID, Caller, RequestID, OperationID, OriginalReceipt, OriginalGeneration, RegistryID, RegistryRevision, OwnerArtifact, AdapterArtifact string
	OperationVersion                                                                                                                                     int
	OriginalDeadline, AcceptedDeadline                                                                                                                   time.Time
	Plan                                                                                                                                                 BudgetPlan
	Claims                                                                                                                                               []string
}
type ReadSettlementAuthorization func(context.Context, ReadSettlementDescriptor) error
type ReadSettlementBlock struct {
	Code, TransactionID, ClaimID, Needed, Route, RouteConstraint string
	RouteAvailable                                               bool
}
type ReadSettlementStatus struct {
	State, Disposition, Certainty, BindingID, RequestID, OriginalReceipt, PreparationID, ResolutionID string
	StateRevision                                                                                     uint64
	RetainedClaims                                                                                    []string
	Blocks                                                                                            []ReadSettlementBlock
}
type SettlementTransaction struct {
	ID, Role, Certainty string
	Receipt             CommitReceipt
}
type SettlementJournal struct{ Key, Anchor, OriginalResolutionID, OriginalUnresolvedID string }
type SettlementClaim struct{ ID, Anchor, CandidateDigest string }

// This private record only adds metadata. Original journals/claims remain intact.
type ReadSettlementRecord struct {
	Dependencies                                                                    []SettlementDependency
	Version, State, PreparationID, ResolutionID, RootAnchor, Disposition, Certainty string
	Journals                                                                        []SettlementJournal
	Claims                                                                          []SettlementClaim
	Transactions                                                                    []SettlementTransaction
}
type settlementVerification struct {
	Revision                                   uint64
	ResolutionID, RecordDigest, SnapshotDigest string
}

func settlementIDs(s *Snapshot, j *JournalEntry) (string, string) {
	identity := payloadDigest([]any{s.BindingID, j.Caller, j.RequestID, j.Digest, j.ReceiptID, j.Generation, j.Deadline, j.Composition.Deadline, j.Composition.Plan, j.Composition.OperationDigest})
	return Hash([]byte("read-settlement/preparation/1\x00" + identity)), Hash([]byte("read-settlement/resolution/1\x00" + identity))
}
func settlementJournalAnchor(j *JournalEntry) string {
	if j == nil {
		return ""
	}
	copy := *j
	copy.ResolutionID = ""
	copy.UnresolvedIntentID = ""
	copy.Intents = map[string]IntentReservation{}
	for id, r := range j.Intents {
		r.State = ""
		copy.Intents[id] = r
	}
	if j.Composition != nil {
		c := *j.Composition
		c.Settlement = nil
		copy.Composition = &c
	}
	return payloadDigest(copy)
}
func settlementClaimAnchor(c *Claim) string {
	if c == nil {
		return ""
	}
	return payloadDigest([]any{c.ID, c.BindingID, c.Caller, c.Epoch, c.Cause, c.OriginRequestID, c.OriginJournalKey, c.DiagnosisID})
}
func settlementIsVerified(s *Snapshot, key string) bool {
	j := s.Journal[key]
	if j == nil || j.Composition == nil || j.Composition.Settlement == nil {
		return false
	}
	r := j.Composition.Settlement
	v, ok := s.settlementVerified[key]
	return ok && r.State == "resolved" && v.Revision == s.StateRevision && v.ResolutionID == r.ResolutionID && v.RecordDigest == payloadDigest(r) && v.SnapshotDigest == payloadDigest(s)
}
func settledJournal(s *Snapshot, key string) bool {
	j := s.Journal[key]
	if j == nil {
		return false
	}
	rootKey := j.CompositionKey
	if j.Composition != nil {
		rootKey = key
	}
	if !settlementIsVerified(s, rootKey) {
		return false
	}
	for _, covered := range s.Journal[rootKey].Composition.Settlement.Journals {
		if covered.Key == key && covered.Anchor == settlementJournalAnchor(j) {
			return true
		}
	}
	return false
}
func validateReadSettlement(s *Snapshot, key string, j *JournalEntry) error {
	r := j.Composition.Settlement
	if r == nil {
		return nil
	}
	prep, resolution := settlementIDs(s, j)
	if r.Version != "read-settlement/1" || (r.State != "prepared" && r.State != "resolved") || r.PreparationID != prep || r.ResolutionID != resolution || prep == resolution || prep == j.Composition.FinishID || resolution == j.Composition.FinishID || r.RootAnchor != settlementJournalAnchor(j) || (r.Certainty != "not_dispatched" && r.Certainty != "completed") || len(r.Journals) < 1 || len(r.Transactions) > 256 {
		return fail(ReconciliationRequired, "settlement_record")
	}
	expected := map[string]bool{key: true}
	for _, id := range []string{j.Composition.InitialID, j.Composition.RecoveryID, j.Composition.RetryID} {
		k := j.Caller + "\x00" + id
		if s.Journal[k] != nil {
			expected[k] = true
		}
	}
	if len(expected) != len(r.Journals) {
		return fail(ReconciliationRequired, "settlement_coverage")
	}
	for _, covered := range r.Journals {
		if !expected[covered.Key] {
			return fail(ReconciliationRequired, "settlement_duplicate_coverage")
		}
		delete(expected, covered.Key)
		child := s.Journal[covered.Key]
		if child == nil || child.Caller != j.Caller || covered.Key != key && child.CompositionKey != key || covered.Anchor != settlementJournalAnchor(child) {
			return fail(ReconciliationRequired, "settlement_journal_anchor")
		}

	}
	claimSet := map[string]bool{}
	for _, covered := range r.Journals {
		if child := s.Journal[covered.Key]; child != nil && child.ClaimID != "" {
			claimSet[child.ClaimID] = true
		}
	}
	if len(claimSet) != len(r.Claims) {
		return fail(ReconciliationRequired, "settlement_claim_coverage")
	}
	for _, ref := range r.Claims {
		if !claimSet[ref.ID] {
			return fail(ReconciliationRequired, "settlement_claim_duplicate")
		}
		delete(claimSet, ref.ID)
		claim := s.Claims[ref.ID]
		if claim == nil || ref.Anchor != settlementClaimAnchor(claim) || ref.CandidateDigest != "" && ref.CandidateDigest != claim.CandidateDigest {
			return fail(ReconciliationRequired, "settlement_claim_anchor")
		}
	}
	return nil
}
func (o *Owner) settlementActive(caller, id, digest string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for key, a := range o.active {
		parts := strings.Split(key, "\x00")
		if len(parts) != 3 || parts[0] != caller || parts[2] != id {
			continue
		}
		a.mu.Lock()
		active := !a.done && a.digest == digest
		a.mu.Unlock()
		if active {
			return true
		}
	}
	return false
}
func (o *Owner) settlementContext(caller context.Context, c *Channel) (context.Context, context.CancelFunc, error) {
	if e := c.check(o); e != nil {
		return nil, nil, e
	}
	if caller.Err() != nil {
		return nil, nil, fail(Canceled, "settlement_control_canceled")
	}
	end := o.now().Add(readSettlementBudget)
	for _, p := range []context.Context{caller, c.lifetime} {
		if d, ok := p.Deadline(); ok && d.Before(end) {
			end = d
		}
	}
	base, stopBase := context.WithCancel(caller)
	stopChannel := context.AfterFunc(c.lifetime, stopBase)
	timed, stopTimed := o.contextUntil(base, end)
	check := func() error {
		if !o.now().Before(end) {
			return fail(Code("deadline_exceeded"), "settlement_control_deadline")
		}
		if timed.Err() != nil || caller.Err() != nil || c.lifetime.Err() != nil {
			return fail(Canceled, "settlement_control_canceled")
		}
		return nil
	}
	ctx := context.WithValue(timed, candidateStorageBoundKey{}, candidateStorageBound{check: check})
	stop := func() { stopChannel(); stopTimed(); stopBase() }
	if e := check(); e != nil {
		stop()
		return nil, nil, e
	}
	return ctx, stop, nil
}
func cloneSettlementDescriptor(d ReadSettlementDescriptor) ReadSettlementDescriptor {
	raw, _ := json.Marshal(d)
	var copy ReadSettlementDescriptor
	json.Unmarshal(raw, &copy)
	return copy
}
func settlementStatus(s *Snapshot, j *JournalEntry, state string) ReadSettlementStatus {
	return ReadSettlementStatus{State: state, BindingID: s.BindingID, RequestID: j.RequestID, OriginalReceipt: j.ReceiptID, StateRevision: s.StateRevision}
}

// settlementControlError copies only a finite safe code/detail vocabulary. Direct
// backend errors never supply a diagnostic detail, even if their Code is known.
// Raw, wrapped and unknown-code errors are storage failures, not wire data.
func settlementControlError(err error, backend bool) *Error {
	if err == nil {
		return nil
	}
	code, detail := StorageFailure, "settlement_control_error"
	if backend {
		detail = "settlement_storage"
	}
	if original, ok := err.(*Error); ok && original != nil {
		candidateCode, candidateDetail := original.Code, original.Detail
		known := true
		switch candidateCode {
		case ProtocolMismatch, UnsafeStorage, StaleGeneration, OwnerBusy,
			BudgetInsufficient, PermissionDenied, StorageFailure,
			ReconciliationRequired, UncertainWrite, Canceled, PayloadTooLarge,
			Code("deadline_exceeded"), Code("unsupported_operation"):
			code = candidateCode
		default:
			known = false
		}
		if known && !backend {
			switch candidateDetail {
			case "settlement_control_error", "settlement_storage",
				"settlement_control_canceled", "settlement_control_deadline",
				"composition_delivery_canceled", "composition_delivery_deadline",
				"settlement_capability_absent", "settlement_operation", "settlement_allow",
				"settlement_active_issuer", "settlement_state", "settlement_scope",
				"settlement_original_allow", "settlement_authorization",
				"missing_original_settlement_anchor", "settlement_artifact_anchor",
				"settlement_identity_plan", "settlement_child_anchor", "settlement_claim_anchor",
				"settlement_claim_origin", "settlement_claim_diagnosis",
				"settlement_diagnosis_outcome", "settlement_claim_diagnosis_missing",
				"channel", "channel_closed", "binding", "confirmation", "operation",
				"schema_invalid", "request", "request_object", "frame", "utf8",
				"trailing", "structure", "json", "key", "duplicate_key", "object",
				"array", "delimiter", "number", "lossless_number":
				detail = candidateDetail
			}
		}
	}
	return &Error{Code: code, Detail: detail}
}

// SettleReadComposition is an explicit host-only metadata capability. It never
// invokes Handle, adapters, recovery, proof, adoption, readiness or business work.
func (o *Owner) SettleReadComposition(caller context.Context, c *Channel, original []byte) (status ReadSettlementStatus, resultErr error) {
	empty := ReadSettlementStatus{}
	ctx, stop, e := o.settlementContext(caller, c)
	if e != nil {
		return empty, settlementControlError(e, false)
	}
	var held *Guard
	defer stop()
	defer func() {
		var releaseErr error
		if held != nil {
			releaseErr = held.Close()
		}
		// Delivery is decided after releasing custody, while this control's
		// lifetime is still live. A blocked status is not a prior body error.
		primary := resultErr
		if primary == nil {
			o.compositionBoundaryPoint("settlement_status")
			primary = o.compositionContextError(ctx)
		}
		if primary != nil {
			safe := settlementControlError(primary, false)
			if releaseErr != nil {
				// Only a fixed/allowlisted detail can reach this finite suffix.
				safe.Detail += "_guard_release"
			}
			status, resultErr = empty, safe
		} else if releaseErr != nil {
			status, resultErr = empty, fail(StorageFailure, "settlement_guard_release")
		}
	}()
	if o.settlementAuthorization == nil {
		return empty, fail(PermissionDenied, "settlement_capability_absent")
	}
	req, e := o.registry.ParseRequest(original)
	if e != nil {
		return empty, e
	}
	if text(req.Value, "kind") != "perform" || req.Operation.Semantics != "safe_read" || req.Operation.ConfirmationRequired {
		return empty, fail(PermissionDenied, "settlement_operation")
	}
	if e = o.auth.Allow(ctx, c.subject, req.Operation, cloneMap(req.Value)); e != nil {
		return empty, fail(PermissionDenied, "settlement_allow")
	}
	if e = checkCandidateStorageBound(ctx); e != nil {
		return empty, e
	}
	if o.settlementActive(c.subject, text(req.Value, "requestId"), req.Digest) {
		return empty, fail(OwnerBusy, "settlement_active_issuer")
	}
	queue, queueStop := o.contextUntil(ctx, o.now().Add(readSettlementQueueBudget))
	g, e := o.backend.Acquire(queue)
	queueStop()
	if e != nil {
		return empty, settlementControlError(e, true)
	}
	held = g
	if o.settlementActive(c.subject, text(req.Value, "requestId"), req.Digest) {
		return empty, fail(OwnerBusy, "settlement_active_issuer")
	}
	s, e := o.backend.Load(ctx, g)
	if e != nil {
		return empty, settlementControlError(e, true)
	}
	if e = o.compositionContextError(ctx); e != nil {
		return empty, e
	}
	key := c.subject + "\x00" + text(req.Value, "requestId")
	if s == nil || s.BindingID != o.registry.BindingID() || s.Validate() != nil || recoveryConsistency(s) != nil {
		return empty, fail(ReconciliationRequired, "settlement_state")
	}
	j := s.Journal[key]
	if j == nil || j.Composition == nil || j.Caller != c.subject || j.RequestID != text(req.Value, "requestId") || j.Digest != req.Digest || j.OperationID != req.Operation.ID || j.OperationVersion != req.Operation.Version || j.Generation != text(req.Value, "expectedGeneration") {
		return empty, fail(PermissionDenied, "settlement_scope")
	}
	req.Deadline = j.Deadline
	req.Value["deadline"] = timestamp(j.Deadline)
	req.acceptedGeneration = j.Generation
	if e = o.auth.Allow(ctx, c.subject, req.Operation, cloneMap(req.Value)); e != nil {
		return empty, fail(PermissionDenied, "settlement_original_allow")
	}
	if e = checkCandidateStorageBound(ctx); e != nil {
		return empty, e
	}
	if e = o.settlementAnchors(s, j, req); e != nil {
		return empty, e
	}
	claims := []string{}
	for _, id := range []string{j.Composition.InitialID, j.Composition.RecoveryID, j.Composition.RetryID} {
		if child := s.Journal[c.subject+"\x00"+id]; child != nil && child.ClaimID != "" && !containsID(claims, child.ClaimID) {
			claims = append(claims, child.ClaimID)
		}
	}
	sort.Strings(claims)
	d := ReadSettlementDescriptor{Action: "settle_read_metadata", BindingID: s.BindingID, Caller: c.subject, RequestID: j.RequestID, OperationID: j.OperationID, OperationVersion: j.OperationVersion, OriginalReceipt: j.ReceiptID, OriginalGeneration: j.Generation, OriginalDeadline: j.Deadline, AcceptedDeadline: j.Composition.Deadline, Plan: j.Composition.Plan, Claims: claims, RegistryID: o.registry.policy.ID, RegistryRevision: o.registry.policy.Revision, OwnerArtifact: o.registry.policy.OwnerArtifactSHA256, AdapterArtifact: j.Composition.AdapterArtifact}
	if e = o.settlementAuthorization(ctx, cloneSettlementDescriptor(d)); e != nil {
		return empty, fail(PermissionDenied, "settlement_authorization")
	}
	if e = checkCandidateStorageBound(ctx); e != nil {
		return empty, e
	}
	if e = c.check(o); e != nil {
		return empty, e
	}
	if j.Composition.Settlement != nil {
		return o.continueReadSettlement(ctx, g, s, key)
	}
	// A committed original outcome is history, never rewritten by settlement.
	r, e := o.backend.Reconcile(ctx, g, j.Composition.FinishID)
	if e != nil || r.OperationID != j.Composition.FinishID {
		return o.blockSettlement(s, j, "original_finish_readback", j.Composition.FinishID, "", "same_settlement_control", true), nil
	}
	if r.Certainty == "committed" {
		loaded, e := o.backend.Load(ctx, g)
		if e != nil {
			return empty, settlementControlError(e, true)
		}
		if e = o.compositionFinalWitness(ctx, g, req, s, loaded, key, r); e != nil {
			return o.blockSettlement(s, j, "original_finish_witness", r.OperationID, "", "same_settlement_control", true), nil
		}
		v, _ := DecodeJSON(loaded.Journal[key].Response, HardFrameLimit)
		out, _ := object(v)
		result := settlementStatus(loaded, loaded.Journal[key], "historical")
		result.Disposition = text(out, "kind")
		result.Certainty = text(out, "certainty")
		return result, nil
	}
	if r.Certainty != "not_committed" || s.Commits[j.Composition.FinishID].OperationID != "" {
		return o.blockSettlement(s, j, "original_finish_unknown", j.Composition.FinishID, "", "same_settlement_control", true), nil
	}
	record, block, e := o.classifyReadSettlement(ctx, g, s, key)
	if e != nil {
		return empty, e
	}
	if block != nil {
		result := settlementStatus(s, j, "blocked")
		result.Blocks = []ReadSettlementBlock{*block}
		return result, nil
	}
	prior, e := o.settlementTransaction(ctx, g, s, record.PreparationID, "terminal_preparation")
	if e != nil || prior.Certainty != "not_committed" {
		return o.blockSettlement(s, j, "terminal_preparation_readback", record.PreparationID, "", "same_settlement_control", true), nil
	}
	desired, e := s.Clone()
	if e != nil {
		return empty, e
	}
	desired.Journal[key].Composition.Settlement = record
	// Stable preparation ID is derivable even if its acknowledgment and later
	// bookkeeping are both lost. Nonpositive preparation never attempts resolution.
	loaded, e := o.saveWithID(ctx, g, desired, record.PreparationID)
	if e != nil {
		return o.blockSettlement(s, j, "terminal_preparation_readback", record.PreparationID, "", "same_settlement_control", true), nil
	}
	return o.continueReadSettlement(ctx, g, loaded, key)
}
func (o *Owner) blockSettlement(s *Snapshot, j *JournalEntry, code, id, claim, route string, available bool) ReadSettlementStatus {
	r := settlementStatus(s, j, "blocked")
	r.Blocks = []ReadSettlementBlock{{Code: code, TransactionID: id, ClaimID: claim, Needed: code, Route: route, RouteAvailable: available}}
	return r
}
