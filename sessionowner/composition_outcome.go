package sessionowner

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
)

func (o *Owner) compositionContextError(ctx context.Context) error {
	if d, ok := ctx.Deadline(); ok && !o.now().Before(d) {
		return fail(Code("deadline_exceeded"), "composition_delivery_deadline")
	}
	if ctx.Err() != nil {
		return fail(Canceled, "composition_delivery_canceled")
	}
	return checkCandidateStorageBound(ctx)
}
func (o *Owner) compositionBoundaryPoint(point string) {
	if o.compositionBoundary != nil {
		o.compositionBoundary(point)
	}
}

// Success linearizes at the final liveness check. Cancellation observed before
// that check wins delivery, not history: a proved committed outcome is retained.
func (o *Owner) deliverCompositionKnown(ctx context.Context, req *Request, raw []byte, point string) ([]byte, error) {
	o.compositionBoundaryPoint(point)
	if e := o.compositionContextError(ctx); e != nil {
		return o.refuseCompositionKnown(req, raw, ErrorCode(e))
	}
	return append([]byte(nil), raw...), nil
}

// A known cache refusal preserves exactly what was proved, including an
// uncertain or not-dispatched result. It never promotes that history to success.
func (o *Owner) refuseCompositionKnown(req *Request, raw []byte, code Code) ([]byte, error) {
	if e := o.registry.ValidateResponse(raw, req); e != nil {
		return nil, e
	}
	v, e := DecodeJSON(raw, HardFrameLimit)
	if e != nil {
		return nil, e
	}
	known, _ := object(v)
	certainty := text(known, "certainty")
	if certainty != "completed" && certainty != "uncertain" && certainty != "not_dispatched" {
		return nil, fail(ReconciliationRequired, "cached_certainty")
	}
	out := o.failureMap(req, nil, nil, code, certainty, text(known, "receiptRef"))
	for _, key := range []string{"acceptedGeneration", "finalGeneration", "stateRevision"} {
		out[key] = known[key]
	}
	result, e := json.Marshal(out)
	if e != nil {
		return nil, e
	}
	return result, o.registry.ValidateResponse(result, req)
}

// This is a post-authorization, memory-only lookup for a late delivery. The
// lock order matches Handle (channel, owner, active entry). No backend access,
// timer renewal or new receipt is needed to refuse an exact proved cache match.
func (o *Owner) refuseLateComposedCache(ctx context.Context, c *Channel, req *Request) ([]byte, bool, error) {
	code := Code("")
	if !req.Deadline.After(o.now()) {
		code = Code("deadline_exceeded")
	} else if late := o.compositionContextError(ctx); late != nil {
		code = ErrorCode(late)
	}
	if code == "" || text(req.Value, "kind") != "perform" || !o.readRecovery[operationKey(req.Operation.ID, req.Operation.Version, "perform")] {
		return nil, false, nil
	}
	c.mu.Lock()
	if c.closed || c.lifetime.Err() != nil {
		c.mu.Unlock()
		return nil, false, nil
	}
	o.mu.Lock()
	cached := o.active[activeKey(c, text(req.Value, "requestId"))]
	if cached == nil {
		o.mu.Unlock()
		c.mu.Unlock()
		return nil, false, nil
	}
	cached.mu.Lock()
	eligible := cached.done && cached.composed && cached.channelRef == c && cached.channel == c.id && cached.digest == req.Digest && len(cached.response) > 0
	raw := []byte(nil)
	receipt, certainty := cached.receipt, cached.certainty
	if eligible {
		raw = append([]byte(nil), cached.response...)
	}
	cached.mu.Unlock()
	o.mu.Unlock()
	c.mu.Unlock()
	if !eligible || o.registry.ValidateResponse(raw, req) != nil {
		return nil, false, nil
	}
	decoded, e := DecodeJSON(raw, HardFrameLimit)
	if e != nil {
		return nil, false, nil
	}
	known, _ := object(decoded)
	if text(known, "receiptRef") != receipt || text(known, "certainty") != certainty || text(known, "acceptedGeneration") != text(req.Value, "expectedGeneration") {
		return nil, false, nil
	}
	o.compositionBoundaryPoint("pre_entry_cache")
	result, e := o.refuseCompositionKnown(req, raw, code)
	return result, true, e
}
func committedOutcomeVisible(s *Snapshot, r CommitReceipt) bool {
	return s != nil && r.Certainty == "committed" && s.StateRevision >= r.StateRevision
}
func receiptPayloadMatches(j *JournalEntry, raw []byte, r CommitReceipt, binding string) bool {
	if j == nil || r.Certainty != "committed" {
		return false
	}
	v, e := DecodeJSON(raw, HardFrameLimit)
	if e != nil {
		return false
	}
	m, ok := object(v)
	if !ok {
		return false
	}
	revision, e := strconv.ParseUint(text(m, "stateRevision"), 10, 64)
	return e == nil && revision == r.StateRevision && text(m, "finalGeneration") == r.Generation && text(m, "acceptedGeneration") == j.Generation && text(m, "requestId") == j.RequestID && text(m, "bindingId") == binding && text(m, "receiptRef") == j.ReceiptID && text(m, "certainty") == j.Certainty
}
func (o *Owner) compositionChildWitness(ctx context.Context, g *Guard, s *Snapshot, rootKey string, root *JournalEntry, child *JournalEntry) (map[string]any, error) {
	if child == nil || child.CompositionKey != rootKey || child.Caller != root.Caller || child.RequestID == root.RequestID || len(child.Digest) != 64 || child.Deadline.After(root.Composition.Deadline) {
		return nil, fail(ReconciliationRequired, "composition_child_link")
	}
	c := root.Composition
	var op Operation
	switch child.RequestID {
	case c.InitialID, c.RetryID:
		op = o.registry.operations[operationKey(root.OperationID, root.OperationVersion, "perform")]
		if child.RequestKind != "perform" {
			return nil, fail(ReconciliationRequired, "composition_child_kind")
		}
	case c.RecoveryID:
		if child.OperationID == o.registry.builtins["recover_new"].ID {
			op = o.registry.builtins["recover_new"]
		} else {
			op = o.registry.builtins["recover_resume"]
		}
		if child.RequestKind != "recover" {
			return nil, fail(ReconciliationRequired, "composition_child_kind")
		}
	default:
		return nil, fail(ReconciliationRequired, "composition_child_id")
	}
	if child.OperationID != op.ID || child.OperationVersion != op.Version || child.Semantics != op.Semantics || child.CompositionBodyCap != int64(op.WireCaps.MaxProviderBodyBytes) {
		return nil, fail(ReconciliationRequired, "composition_child_operation")
	}
	req := &Request{Value: map[string]any{"kind": child.RequestKind, "requestId": child.RequestID, "expectedGeneration": child.Generation}, Operation: op, acceptedGeneration: child.Generation}
	raw := child.Response
	id := child.ResponseCommitID
	if child.RequestKind == "recover" && id == "" {
		claim := s.Claims[child.ClaimID]
		if claim == nil || claim.Epoch != root.Generation || claim.Caller != root.Caller {
			return nil, fail(ReconciliationRequired, "composition_claim_link")
		}
		claimReceipt, exists := s.Commits[claim.CommitOperationID]
		if !exists || !committedOutcomeVisible(s, claimReceipt) {
			return nil, fail(ReconciliationRequired, "composition_claim_revision")
		}
		known, e := o.acceptedReadback(ctx, g, req, child.Caller, s, claim)
		if e != nil {
			return nil, e
		}
		v, e := DecodeJSON(known, HardFrameLimit)
		if e != nil {
			return nil, e
		}
		out, _ := object(v)
		if text(out, "kind") != "recovered" {
			return nil, fail(ReconciliationRequired, "composition_claim_witness")
		}
		if e = o.compositionContextError(ctx); e != nil {
			return nil, e
		}
		return out, nil
	}
	if !validID(id) || len(raw) == 0 || !bytes.Equal(raw, child.PendingResponse) {
		return nil, fail(ReconciliationRequired, "composition_child_payload")
	}
	if e := o.compositionContextError(ctx); e != nil {
		return nil, e
	}
	r, e := o.backend.Reconcile(ctx, g, id)
	stored, ok := s.Commits[id]
	if e != nil || !ok || !committedOutcomeVisible(s, r) || !receiptMatches(r, stored) || !receiptPayloadMatches(child, raw, r, s.BindingID) || o.registry.ValidateResponse(raw, req) != nil {
		return nil, fail(ReconciliationRequired, "composition_child_receipt")
	}
	if e = o.compositionContextError(ctx); e != nil {
		return nil, e
	}
	v, _ := DecodeJSON(raw, HardFrameLimit)
	out, _ := object(v)
	return out, nil
}
func (o *Owner) compositionFinalWitness(ctx context.Context, g *Guard, req *Request, observed, loaded *Snapshot, key string, r CommitReceipt) error {
	before := observed.Journal[key]
	if before == nil || before.Composition == nil || loaded == nil || loaded.Validate() != nil || recoveryConsistency(loaded) != nil || loaded.Generation != observed.Generation {
		return fail(ReconciliationRequired, "composition_snapshot")
	}
	if !committedOutcomeVisible(loaded, r) {
		return fail(ReconciliationRequired, "composition_preceding_revision")
	}
	record := loaded.Journal[key]
	c := before.Composition
	if record == nil || record.Composition == nil || record.Composition.Stage != "done" || r.OperationID != c.FinishID {
		return fail(ReconciliationRequired, "composition_final_identity")
	}
	if c.Stage != "done" && c.Stage != "final_prepared" {
		return fail(ReconciliationRequired, "composition_final_phase")
	}
	if c.Stage == "done" {
		if !sameSnapshot(observed, loaded) {
			return fail(ReconciliationRequired, "composition_snapshot_substitution")
		}
	} else {
		desired, e := observed.Clone()
		if e != nil {
			return e
		}
		j := desired.Journal[key]
		j.Composition.Stage = "done"
		j.Response = append([]byte(nil), c.FinalPayload...)
		v, e := DecodeJSON(c.FinalPayload, HardFrameLimit)
		if e != nil {
			return e
		}
		out, _ := object(v)
		j.Certainty = text(out, "certainty")
		j.Phase = "EXECUTE_RESULT"
		if !matchingReadback(loaded, desired, r) {
			return fail(ReconciliationRequired, "composition_prepared_witness")
		}
	}
	stored, ok := loaded.Commits[c.FinishID]
	if !ok || !receiptMatches(r, stored) || !bytes.Equal(record.Response, c.FinalPayload) || !bytes.Equal(record.Composition.FinalPayload, c.FinalPayload) || !receiptPayloadMatches(record, record.Response, r, loaded.BindingID) || o.registry.ValidateResponse(record.Response, req) != nil {
		return fail(ReconciliationRequired, "composition_final_payload")
	}
	// At the exact committed revision the native receipt's full-state digest is
	// reconstructible. Later historical revisions retain the original receipt.
	if loaded.StateRevision == r.StateRevision {
		desired, e := loaded.Clone()
		if e != nil {
			return e
		}
		desired.StateRevision--
		desired.Commits = map[string]CommitReceipt{}
		raw, _ := json.Marshal(desired)
		if Hash(raw) != r.Digest {
			return fail(ReconciliationRequired, "composition_final_digest")
		}
	}
	v, _ := DecodeJSON(record.Response, HardFrameLimit)
	out, _ := object(v)
	if text(out, "kind") == "performed" {
		terminalID := c.InitialID
		if c.RetryReserved {
			terminalID = c.RetryID
		}
		terminal := loaded.Journal[record.Caller+"\x00"+terminalID]
		child, e := o.compositionChildWitness(ctx, g, loaded, key, record, terminal)
		if e != nil || text(child, "kind") != "performed" || !equalJSON(child["result"], out["result"]) || text(child, "finalGeneration") != text(out, "finalGeneration") {
			return fail(ReconciliationRequired, "composition_terminal_witness")
		}
		if c.RetryReserved {
			initial := loaded.Journal[record.Caller+"\x00"+c.InitialID]
			if initial == nil || initial.Diagnosis == nil || initial.Diagnosis.Epoch != record.Generation {
				return fail(ReconciliationRequired, "composition_diagnosis_witness")
			}
			if _, e = o.compositionChildWitness(ctx, g, loaded, key, record, initial); e != nil {
				return e
			}
			recovery := loaded.Journal[record.Caller+"\x00"+c.RecoveryID]
			recovered, e := o.compositionChildWitness(ctx, g, loaded, key, record, recovery)
			if e != nil || text(recovered, "kind") != "recovered" || text(recovered, "finalGeneration") != terminal.Generation {
				return fail(ReconciliationRequired, "composition_adoption_witness")
			}
			claim := loaded.Claims[recovery.ClaimID]
			if claim == nil || payloadDigest(claim.Proof) != c.IdentityDigest {
				return fail(ReconciliationRequired, "composition_identity_witness")
			}
		}
	}
	return o.compositionContextError(ctx)
}
