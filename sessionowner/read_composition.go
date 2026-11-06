package sessionowner

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// ReadComposition is private progress on the ORIGINAL Perform journal. It is not
// another manager, store, public request or permission to renew a lost parent.
type ReadComposition struct {
	RegistryPolicyDigest string                `json:"registryPolicyDigest"`
	AdapterArtifact      string                `json:"adapterArtifact"`
	Settlement           *ReadSettlementRecord `json:"settlement"`
	Plan                 BudgetPlan            `json:"plan"`
	Deadline             time.Time             `json:"deadline"`
	OperationDigest      string                `json:"operationDigest"`
	RecoveryNewDigest    string                `json:"recoveryNewDigest"`
	RecoveryResumeDigest string                `json:"recoveryResumeDigest"`
	IdentityDigest       string                `json:"identityDigest"`
	InitialID            string                `json:"initialId"`
	RecoveryID           string                `json:"recoveryId"`
	RetryID              string                `json:"retryId"`
	Stage                string                `json:"stage"`
	RetryReserved        bool                  `json:"retryReserved"`
	FinishID             string                `json:"finishId"`
	FinalPayload         []byte                `json:"finalPayload"`
}

func sameCompositionTransport(a, b Operation) bool {
	return a.AdapterID != nil && b.AdapterID != nil && *a.AdapterID == *b.AdapterID && a.Transport == b.Transport && equalJSON(normalize(a.TransportDescriptorID), normalize(b.TransportDescriptorID))
}
func validateReadRecoveryOptions(options OwnerOptions) (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, ref := range options.ReadRecoveryOperations {
		key := operationKey(ref.ID, ref.Version, "perform")
		op, ok := options.Registry.operations[key]
		n, nok := options.Registry.builtins["recover_new"]
		r, rok := options.Registry.builtins["recover_resume"]
		if !ok || op.Semantics != "safe_read" || op.ConfirmationRequired || allowed[key] || options.ReadRecoveryAuthorization == nil || !nok || !rok || !sameCompositionTransport(op, n) || !sameCompositionTransport(op, r) {
			return nil, fail(ProtocolMismatch, "read_recovery_binding")
		}
		allowed[key] = true
	}
	return allowed, nil
}
func validateReadCompositions(s *Snapshot) error {
	for key, j := range s.Journal {
		if j == nil {
			return fail(ReconciliationRequired, "composition_journal")
		}
		if c := j.Composition; c != nil {
			if e := validateReadSettlement(s, key, j); e != nil {
				return e
			}
			if j.RequestKind != "perform" || j.Semantics != "safe_read" || j.CompositionKey != "" || c.Plan.MaxSafeReadRetries != 1 || c.Deadline.IsZero() || c.Deadline.After(j.Deadline) || !validID(c.InitialID) || !validID(c.RecoveryID) || !validID(c.RetryID) || !validID(c.FinishID) || c.InitialID == c.RecoveryID || c.InitialID == c.RetryID || c.RecoveryID == c.RetryID || len(c.OperationDigest) != 64 || len(c.IdentityDigest) != 64 {
				return fail(ReconciliationRequired, "composition_binding")
			}
			if c.Stage != "initial" && c.Stage != "recovery" && c.Stage != "retry" && c.Stage != "final_prepared" && c.Stage != "done" {
				return fail(ReconciliationRequired, "composition_stage")
			}
			if (c.Stage == "done" || c.Stage == "final_prepared") != (len(c.FinalPayload) > 0) {
				return fail(ReconciliationRequired, "composition_final_preparation")
			}
			if c.Stage == "done" && (len(c.FinalPayload) == 0 || !bytes.Equal(c.FinalPayload, j.Response)) {
				return fail(ReconciliationRequired, "composition_result")
			}
			if c.RetryReserved != (s.Journal[j.Caller+"\x00"+c.RetryID] != nil) {
				return fail(ReconciliationRequired, "composition_retry")
			}
			requests, reserved, e := compositionUsage(s, key)
			if e != nil || requests > c.Plan.MaxProviderRequests || reserved > c.Plan.MaxProviderBytes {
				return fail(ReconciliationRequired, "composition_counters")
			}
		}
		if j.CompositionKey != "" {
			root := s.Journal[j.CompositionKey]
			if root == nil || root.Composition == nil || root.Caller != j.Caller || j.Deadline.After(root.Composition.Deadline) || j.Composition != nil {
				return fail(ReconciliationRequired, "composition_child")
			}
			c := root.Composition
			if j.RequestID != c.InitialID && j.RequestID != c.RecoveryID && j.RequestID != c.RetryID {
				return fail(ReconciliationRequired, "composition_child_id")
			}
			if j.ChildAdmittedAt.IsZero() || j.ChildMaxTotalMs <= 0 || j.Deadline.After(j.ChildAdmittedAt.Add(time.Duration(j.ChildMaxTotalMs)*time.Millisecond)) {
				return fail(ReconciliationRequired, "child_native_deadline")
			}
			if j.CompositionBodyCap <= 0 || j.CompositionBodyCap > HardFrameLimit || int64(j.Dispatches) > math.MaxInt64/j.CompositionBodyCap || j.ProviderBytesReserved != int64(j.Dispatches)*j.CompositionBodyCap {
				return fail(ReconciliationRequired, "composition_byte_witness")
			}
			if j.ProviderBytesReserved < 0 || j.Dispatches < 0 {
				return fail(ReconciliationRequired, "composition_usage")
			}
		}
	}
	return nil
}
func compositionUsage(s *Snapshot, key string) (int, int64, error) {
	count := 0
	var reserved int64
	for _, j := range s.Journal {
		if j != nil && j.CompositionKey == key {
			if j.Dispatches < 0 || j.Dispatches > math.MaxInt-count || j.ProviderBytesReserved < 0 || j.ProviderBytesReserved > math.MaxInt64-reserved {
				return 0, 0, fail(ReconciliationRequired, "composition_overflow")
			}
			count += j.Dispatches
			reserved += j.ProviderBytesReserved
		}
	}
	return count, reserved, nil
}
func (i *Invocation) checkCompositionPermit() error {
	if i.compositionKey == "" {
		return nil
	}
	j := i.snapshot.Journal[i.compositionKey]
	if j == nil || j.Composition == nil {
		return fail(ReconciliationRequired, "composition_missing")
	}
	c := j.Composition
	if c.Settlement != nil {
		return fail(ReconciliationRequired, "settled_read_never_continues")
	}
	if !i.owner.now().Before(c.Deadline) || i.operationCtx != nil && i.operationCtx.Err() != nil {
		return fail(Canceled, "composition_lifetime")
	}
	used, reserved, e := compositionUsage(i.snapshot, i.compositionKey)
	if e != nil {
		return e
	}
	body := int64(i.request.Operation.WireCaps.MaxProviderBodyBytes)
	if child := i.snapshot.Journal[i.journalKey]; child == nil || child.CompositionBodyCap != body {
		return fail(ReconciliationRequired, "composition_native_body_cap")
	}
	if used >= c.Plan.MaxProviderRequests || body > c.Plan.MaxProviderBytes-reserved {
		return fail(BudgetInsufficient, "composition_reservation")
	}
	return nil
}
func minCompositionPlan(child, root BudgetPlan) BudgetPlan {
	if root.MaxProviderRequests < child.MaxProviderRequests {
		child.MaxProviderRequests = root.MaxProviderRequests
	}
	if root.MaxProviderBytes < child.MaxProviderBytes {
		child.MaxProviderBytes = root.MaxProviderBytes
	}
	if root.MaxAcquisitionAttempts < child.MaxAcquisitionAttempts {
		child.MaxAcquisitionAttempts = root.MaxAcquisitionAttempts
	}
	if root.MaxProofAttempts < child.MaxProofAttempts {
		child.MaxProofAttempts = root.MaxProofAttempts
	}
	if root.MaxReadinessResumptions < child.MaxReadinessResumptions {
		child.MaxReadinessResumptions = root.MaxReadinessResumptions
	}
	if root.MaxTotalMs < child.MaxTotalMs {
		child.MaxTotalMs = root.MaxTotalMs
	}
	phases := map[string]int64{}
	for k, v := range child.PhaseMaxMs {
		if cap, ok := root.PhaseMaxMs[k]; !ok || cap < v {
			v = cap
		}
		phases[k] = v
	}
	child.PhaseMaxMs = phases
	if root.MaxRPCMs != nil && (child.MaxRPCMs == nil || *root.MaxRPCMs < *child.MaxRPCMs) {
		v := *root.MaxRPCMs
		child.MaxRPCMs = &v
	}
	if root.MaxSingleProviderRequestMs != nil && (child.MaxSingleProviderRequestMs == nil || *root.MaxSingleProviderRequestMs < *child.MaxSingleProviderRequestMs) {
		v := *root.MaxSingleProviderRequestMs
		child.MaxSingleProviderRequestMs = &v
	}
	return child
}
func compositionTail(p BudgetPlan) time.Duration {
	return time.Duration(phaseReserve("action", p.PhaseMaxMs["action"])+p.PhaseMaxMs["commit"]+p.PhaseMaxMs["cleanup"]+p.PhaseMaxMs["ipc"]) * time.Millisecond
}
func (o *Owner) compositionChild(ctx context.Context, root *Invocation, value map[string]any, plan BudgetPlan, stage string) (*Invocation, error) {
	started := o.now()
	raw, e := json.Marshal(value)
	if e != nil {
		return nil, e
	}
	req, e := o.registry.ParseRequest(raw)
	if e != nil {
		return nil, e
	}
	bounded, stop, e := o.admitCompositionChild(ctx, root, req, plan, stage, started)
	if e != nil {
		return nil, e
	}
	returned := false
	defer func() {
		if !returned {
			stop()
		}
	}()
	ctx = bounded
	if text(req.Value, "kind") == "recover" {
		copyReq := *root.request
		copyReq.Value = cloneMap(root.request.Value)
		copyReq.Operation = cloneOperation(root.request.Operation)
		ref, authErr := o.readRecoveryAuthorization(ctx, root.active.channelRef.subject, &copyReq)
		if authErr != nil || !validID(ref) {
			return nil, fail(PermissionDenied, "composition_approval_reference")
		}
		if e = checkCandidateStorageBound(ctx); e != nil {
			return nil, e
		}
		req.Value["recoveryAuthorizationRef"] = ref
		encoded, _ := json.Marshal(req.Value)
		req, e = o.registry.ParseRequest(encoded)
		if e != nil {
			return nil, e
		}
	}
	req.acceptedGeneration = root.snapshot.Generation
	if e = o.auth.Allow(ctx, root.active.channelRef.subject, req.Operation, cloneMap(req.Value)); e != nil {
		return nil, fail(PermissionDenied, "composition_child_authorization")
	}
	if text(req.Value, "kind") == "recover" {
		if e = o.auth.Recovery(ctx, root.active.channelRef.subject, text(req.Value, "recoveryAuthorizationRef"), req); e != nil {
			return nil, fail(PermissionDenied, "composition_recovery_authorization")
		}
	}
	if e = o.compositionChildReserve(ctx, root, req, plan, stage); e != nil {
		return nil, e
	}
	key := root.active.channelRef.subject + "\x00" + text(req.Value, "requestId")
	if root.snapshot.Journal[key] != nil {
		return nil, fail(ReconciliationRequired, "composition_child_replay")
	}
	desired, e := root.snapshot.Clone()
	if e != nil {
		return nil, e
	}
	c := desired.Journal[root.journalKey].Composition
	c.Stage = stage
	if stage == "retry" {
		if c.RetryReserved {
			return nil, fail(ReconciliationRequired, "composition_retry_used")
		}
		c.RetryReserved = true
	}
	receipt := newID()
	desired.Journal[key] = &JournalEntry{ChildAdmittedAt: started, ChildMaxTotalMs: plan.MaxTotalMs, CompositionBodyCap: int64(req.Operation.WireCaps.MaxProviderBodyBytes), CompositionKey: root.journalKey, Generation: root.snapshot.Generation, OperationID: req.Operation.ID, OperationVersion: req.Operation.Version, RequestKind: text(req.Value, "kind"), Caller: root.active.channelRef.subject, RequestID: text(req.Value, "requestId"), Digest: req.Digest, Semantics: req.Operation.Semantics, Certainty: "not_dispatched", Phase: "EXECUTE_READY", ReceiptID: receipt, Deadline: req.Deadline}
	loaded, e := o.save(ctx, root.guard, desired)
	if e != nil {
		return nil, e
	}
	root.installSnapshot(loaded)
	if e = o.compositionChildReserve(ctx, root, req, plan, stage); e != nil {
		return nil, e
	}
	active := &activeOperation{channelRef: root.active.channelRef, channel: root.active.channel, receipt: receipt, digest: req.Digest, certainty: "not_dispatched"}
	returned = true
	return &Invocation{releaseContext: stop, parentActive: root.active, compositionKey: root.journalKey, operationCtx: ctx, outerDeadline: req.Deadline, owner: o, request: req, plan: plan, guard: root.guard, snapshot: loaded, journalKey: key, active: active, step: "execute", Context: cloneMap(loaded.Credentials), Target: mapValue(req.Value["target"]), Args: mapValue(req.Value["args"])}, nil
}
func (o *Owner) beginComposition(ctx context.Context, root *Invocation) ([]byte, error) {
	deadline, _ := ctx.Deadline()
	n, r := o.registry.builtins["recover_new"], o.registry.builtins["recover_resume"]
	record := &ReadComposition{RegistryPolicyDigest: payloadDigest(o.registry.policy), AdapterArtifact: o.registry.adapters[*root.request.Operation.AdapterID].ArtifactSHA256, Plan: root.plan, Deadline: deadline, OperationDigest: payloadDigest(root.request.Operation), RecoveryNewDigest: payloadDigest(n), RecoveryResumeDigest: payloadDigest(r), IdentityDigest: payloadDigest(root.snapshot.Identity), InitialID: newID(), RecoveryID: newID(), RetryID: newID(), Stage: "initial", FinishID: newID()}
	root.snapshot.Journal[root.journalKey].Composition = record
	loaded, e := o.save(ctx, root.guard, root.snapshot)
	if e != nil {
		return o.failed(root.request, nil, nil, StorageFailure, "uncertain", root.active.receipt)
	}
	root.installSnapshot(loaded)
	// Reuse the existing strict private storage bound: this opt-in sequence never
	// detaches canceled work or borrows time beyond its original accepted window.
	original := ctx
	check := func() error {
		if original.Err() != nil || root.active.channelRef.lifetime.Err() != nil {
			return fail(Canceled, "composition_lifetime")
		}
		if !o.now().Before(deadline) {
			return fail(Code("deadline_exceeded"), "composition_deadline")
		}
		return nil
	}
	ctx = context.WithValue(ctx, candidateStorageBoundKey{}, candidateStorageBound{check: check})
	root.operationCtx = ctx
	value := cloneMap(root.request.Value)
	value["requestId"] = record.InitialID
	value["deadline"] = timestamp(deadline)
	child, e := o.compositionChild(ctx, root, value, root.plan, "initial")
	if e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "uncertain")
	}
	defer child.releaseContext()
	raw, e := o.performInvocation(child.operationCtx, child)
	root.installSnapshot(child.snapshot)
	if e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "uncertain")
	}
	v, e := DecodeJSON(raw, HardFrameLimit)
	if e != nil {
		return o.compositionFailure(ctx, root, ProtocolMismatch, "uncertain")
	}
	out, _ := object(v)
	if text(out, "kind") == "performed" {
		return o.finishComposition(ctx, root, out)
	}
	j := root.snapshot.Journal[child.journalKey]
	failure, _ := object(out["failure"])
	if j.Diagnosis == nil || j.Certainty != "completed" || text(out, "certainty") != "completed" || (text(failure, "category") != "needs_login" && text(failure, "category") != "rejected_generation") {
		return o.finishComposition(ctx, root, out)
	}
	if e = check(); e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "completed")
	}
	return o.composeRecovery(ctx, root, j.Diagnosis.Cause, record)
}
func (o *Owner) composeRecovery(ctx context.Context, root *Invocation, cause string, c *ReadComposition) ([]byte, error) {
	var claim *Claim
	for _, candidate := range root.snapshot.Claims {
		if candidate.Epoch == root.snapshot.Generation && candidate.Cause == cause {
			if claim != nil || candidate.Caller != root.active.channelRef.subject {
				return o.compositionFailure(ctx, root, ReconciliationRequired, "completed")
			}
			claim = candidate
		}
	}
	mode := "new"
	if claim != nil {
		mode = "resume"
	}
	ref := "owner-authorization-pending" // Replaced under the native bound before approval or storage.
	end := c.Deadline.Add(-compositionTail(root.plan))
	if !end.After(o.now()) {
		return o.compositionFailure(ctx, root, BudgetInsufficient, "completed")
	}
	value := map[string]any{"protocol": Protocol, "kind": "recover", "requestId": c.RecoveryID, "bindingId": root.snapshot.BindingID, "deadline": timestamp(end), "mode": mode, "expectedGeneration": root.snapshot.Generation, "recoveryAuthorizationRef": ref}
	if mode == "new" {
		value["cause"] = cause
		value["rejectedRequestId"] = c.InitialID
	} else {
		value["claimId"] = claim.ID
	}
	encoded, _ := json.Marshal(value)
	request, e := o.registry.ParseRequest(encoded)
	if e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "completed")
	}
	phases := []string{"commit", "cleanup", "ipc"}
	if claim == nil || claim.Candidate == nil {
		phases = append(phases, "startup", "capture")
	}
	if claim == nil || claim.Proof == nil {
		phases = append(phases, "proof")
	}
	plan, e := ReservePlan(request, o.now(), o.warm(), phases)
	if e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "completed")
	}
	plan = minCompositionPlan(plan, root.plan)
	needed := 1
	neededBytes := int64(root.request.Operation.WireCaps.MaxProviderBodyBytes)
	if claim == nil || claim.Candidate == nil {
		if plan.MaxAcquisitionAttempts < 1 || plan.PhaseMaxMs["capture"] <= 0 {
			return o.compositionFailure(ctx, root, BudgetInsufficient, "completed")
		}
		needed++
		neededBytes += int64(request.Operation.WireCaps.MaxProviderBodyBytes)
	}
	if claim == nil || claim.Proof == nil {
		if plan.MaxProofAttempts < 1 || plan.PhaseMaxMs["proof"] <= 0 {
			return o.compositionFailure(ctx, root, BudgetInsufficient, "completed")
		}
		needed++
		neededBytes += int64(request.Operation.WireCaps.MaxProviderBodyBytes)
	}
	if !o.warm() && plan.Readiness != "none" {
		if plan.PhaseMaxMs["startup"] <= 0 {
			return o.compositionFailure(ctx, root, BudgetInsufficient, "completed")
		}
		needed++
		neededBytes += int64(request.Operation.WireCaps.MaxProviderBodyBytes)
	}
	used, reserved, e := compositionUsage(root.snapshot, root.journalKey)
	if e != nil || needed > root.plan.MaxProviderRequests-used || neededBytes > root.plan.MaxProviderBytes-reserved {
		return o.compositionFailure(ctx, root, BudgetInsufficient, "completed")
	}
	for _, fence := range root.snapshot.Fences {
		if fence.After(o.now()) {
			return o.compositionFailure(ctx, root, Code("throttled"), "completed")
		}
	}
	childCtx, stop := o.contextUntil(ctx, end)
	defer stop()
	child, e := o.compositionChild(childCtx, root, value, plan, "recovery")
	if e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "uncertain")
	}
	defer child.releaseContext()
	childCtx = child.operationCtx
	raw, e := o.recover(childCtx, child)
	if boundErr := checkCandidateStorageBound(childCtx); boundErr != nil {
		return o.compositionFailure(ctx, root, ErrorCode(boundErr), "uncertain")
	}
	loaded, loadErr := o.backend.Load(childCtx, root.guard)
	if loadErr != nil {
		return o.failed(root.request, nil, nil, StorageFailure, "uncertain", root.active.receipt)
	}
	root.installSnapshot(loaded)
	if e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "uncertain")
	}
	decoded, e := DecodeJSON(raw, HardFrameLimit)
	if e != nil {
		return o.compositionFailure(ctx, root, ProtocolMismatch, "uncertain")
	}
	out, _ := object(decoded)
	if text(out, "kind") != "recovered" {
		return o.finishComposition(ctx, root, out)
	}
	journal := loaded.Journal[child.journalKey]
	if journal == nil {
		return o.compositionFailure(ctx, root, ReconciliationRequired, "uncertain")
	}
	accepted := loaded.Claims[journal.ClaimID]
	if accepted == nil || accepted.Phase != "ACCEPTED" || accepted.ProposedGeneration != loaded.Generation || payloadDigest(loaded.Identity) != c.IdentityDigest {
		return o.compositionFailure(ctx, root, ReconciliationRequired, "uncertain")
	}
	receipt, e := o.backend.Reconcile(childCtx, root.guard, accepted.CommitOperationID)
	stored, ok := loaded.Commits[accepted.CommitOperationID]
	if e != nil || !ok || !receiptMatches(receipt, stored) {
		return o.compositionFailure(ctx, root, ReconciliationRequired, "uncertain")
	}
	if boundErr := checkCandidateStorageBound(childCtx); boundErr != nil {
		return o.compositionFailure(ctx, root, ErrorCode(boundErr), "completed")
	}
	if ctx.Err() != nil || !o.now().Before(c.Deadline) {
		return o.compositionFailure(ctx, root, Canceled, "completed")
	}
	if c.Deadline.Sub(o.now()) < compositionTail(root.plan) {
		return o.compositionFailure(ctx, root, BudgetInsufficient, "completed")
	}
	retry := cloneMap(root.request.Value)
	retry["requestId"] = c.RetryID
	retry["expectedGeneration"] = loaded.Generation
	retry["deadline"] = timestamp(c.Deadline)
	read, e := o.compositionChild(ctx, root, retry, root.plan, "retry")
	if e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "uncertain")
	}
	defer read.releaseContext()
	raw, e = o.performInvocation(read.operationCtx, read)
	root.installSnapshot(read.snapshot)
	if e != nil {
		return o.compositionFailure(ctx, root, ErrorCode(e), "uncertain")
	}
	decoded, e = DecodeJSON(raw, HardFrameLimit)
	if e != nil {
		return o.compositionFailure(ctx, root, ProtocolMismatch, "uncertain")
	}
	out, _ = object(decoded)
	return o.finishComposition(ctx, root, out)
}
func (o *Owner) compositionFailure(ctx context.Context, root *Invocation, code Code, certainty string) ([]byte, error) {
	if ctx.Err() != nil || !o.now().Before(root.snapshot.Journal[root.journalKey].Composition.Deadline) {
		return o.failed(root.request, root.snapshot, nil, code, certainty, root.active.receipt)
	}
	return o.finishComposition(ctx, root, o.failureMap(root.request, root.snapshot, nil, code, certainty, root.active.receipt))
}
func (o *Owner) finishComposition(ctx context.Context, root *Invocation, value map[string]any) ([]byte, error) {
	if ctx.Err() != nil || checkCandidateStorageBound(ctx) != nil {
		return o.failed(root.request, root.snapshot, nil, Canceled, "uncertain", root.active.receipt)
	}
	out := cloneMap(value)
	out["requestId"] = text(root.request.Value, "requestId")
	out["receiptRef"] = root.active.receipt
	out["acceptedGeneration"] = root.request.acceptedGeneration
	if text(out, "kind") == "failed" {
		out["requestKind"] = "perform"
	}
	out["stateRevision"] = strconv.FormatUint(root.snapshot.StateRevision+2, 10)
	raw, e := json.Marshal(out)
	if e != nil {
		return nil, e
	}
	if e = o.registry.ValidateResponse(raw, root.request); e != nil {
		return nil, e
	}
	desired, e := root.snapshot.Clone()
	if e != nil {
		return nil, e
	}
	c := desired.Journal[root.journalKey].Composition
	c.FinalPayload = raw
	c.Stage = "final_prepared"
	loaded, e := o.save(ctx, root.guard, desired)
	if e != nil {
		if canceled := o.compositionContextError(ctx); canceled != nil {
			return o.failed(root.request, nil, nil, ErrorCode(canceled), "uncertain", root.active.receipt)
		}
		return o.failed(root.request, nil, nil, StorageFailure, "uncertain", root.active.receipt)
	}
	root.installSnapshot(loaded)
	desired, e = loaded.Clone()
	if e != nil {
		return nil, e
	}
	j := desired.Journal[root.journalKey]
	j.Composition.Stage = "done"
	j.Response = raw
	j.Certainty = text(out, "certainty")
	j.Phase = "EXECUTE_RESULT"
	loaded, e = o.saveWithID(ctx, root.guard, desired, j.Composition.FinishID)
	if e != nil {
		if canceled := o.compositionContextError(ctx); canceled != nil {
			return o.failed(root.request, nil, nil, ErrorCode(canceled), "uncertain", root.active.receipt)
		}
		return o.failed(root.request, nil, nil, StorageFailure, "uncertain", root.active.receipt)
	}
	root.installSnapshot(loaded)
	o.compositionBoundaryPoint("live_readback")
	if o.compositionContextError(ctx) != nil {
		root.active.mu.Lock()
		root.active.certainty = j.Certainty
		root.active.mu.Unlock()
		return o.deliverCompositionKnown(ctx, root.request, raw, "live_refused")
	}
	root.active.mu.Lock()
	root.active.response = append([]byte(nil), raw...)
	root.active.certainty = j.Certainty
	root.active.mu.Unlock()
	return o.deliverCompositionKnown(ctx, root.request, raw, "live_return")
}

// Restart is deliberately readback-only: a new context cannot attest that the
// original parent invocation is still live. It never starts a read or recovery.
func (o *Owner) readComposition(ctx context.Context, g *Guard, req *Request, caller string, s *Snapshot, key string) ([]byte, error) {
	j := s.Journal[key]
	c := j.Composition
	req.acceptedGeneration = j.Generation
	if c.Settlement != nil {
		return o.readSettledComposition(ctx, g, req, s, key)
	}
	if caller != j.Caller || payloadDigest(req.Operation) != c.OperationDigest || payloadDigest(o.registry.builtins["recover_new"]) != c.RecoveryNewDigest || payloadDigest(o.registry.builtins["recover_resume"]) != c.RecoveryResumeDigest {
		return o.failed(req, s, nil, ReconciliationRequired, "uncertain", j.ReceiptID)
	}
	if e := o.compositionContextError(ctx); e != nil {
		return o.failed(req, s, nil, ErrorCode(e), "uncertain", j.ReceiptID)
	}
	if len(c.FinalPayload) > 0 {
		r, e := o.backend.Reconcile(ctx, g, c.FinishID)
		if e == nil && r.OperationID == c.FinishID && r.Certainty == "committed" {
			if e = o.compositionContextError(ctx); e != nil {
				return o.failed(req, s, nil, ErrorCode(e), "uncertain", j.ReceiptID)
			}
			loaded, err := o.backend.Load(ctx, g)
			if err == nil {
				if err = o.compositionFinalWitness(ctx, g, req, s, loaded, key, r); err == nil {
					return o.deliverCompositionKnown(ctx, req, loaded.Journal[key].Response, "history_return")
				} else if o.compositionContextError(ctx) != nil {
					return o.failed(req, s, nil, ErrorCode(o.compositionContextError(ctx)), "uncertain", j.ReceiptID)
				}
			}
		}
		return o.failed(req, s, nil, ReconciliationRequired, "uncertain", j.ReceiptID)
	}
	for _, child := range s.Journal {
		if child.CompositionKey != key {
			continue
		}
		if e := o.compositionContextError(ctx); e != nil {
			return o.failed(req, s, nil, ErrorCode(e), "uncertain", j.ReceiptID)
		}
		if child.ResponseCommitID != "" {
			r, e := o.backend.Reconcile(ctx, g, child.ResponseCommitID)
			if e != nil || r.OperationID != child.ResponseCommitID || r.Certainty == "uncertain" {
				return o.failed(req, s, nil, ReconciliationRequired, "uncertain", j.ReceiptID)
			}
		}
		for id := range child.Intents {
			r, e := o.backend.Reconcile(ctx, g, id)
			if e != nil || r.OperationID != id || r.Certainty == "uncertain" {
				return o.failed(req, s, nil, ReconciliationRequired, "uncertain", j.ReceiptID)
			}
		}
		if claim := s.Claims[child.ClaimID]; claim != nil && claim.CommitOperationID != "" {
			r, e := o.backend.Reconcile(ctx, g, claim.CommitOperationID)
			if e != nil || r.OperationID != claim.CommitOperationID || r.Certainty == "uncertain" {
				return o.failed(req, s, claim, ReconciliationRequired, "uncertain", j.ReceiptID)
			}
		}
	}
	code := ReconciliationRequired
	if !o.now().Before(c.Deadline) {
		code = Code("deadline_exceeded")
	}
	return o.failed(req, s, nil, code, "uncertain", j.ReceiptID)
}

func compositionRecoveryReserve(p BudgetPlan) time.Duration {
	return time.Duration(p.PhaseMaxMs["startup"]+p.PhaseMaxMs["capture"]+p.PhaseMaxMs["proof"]+p.PhaseMaxMs["commit"]+p.PhaseMaxMs["cleanup"]+p.PhaseMaxMs["ipc"])*time.Millisecond + compositionTail(p)
}
func (o *Owner) finishCompositionChild(ctx context.Context, i *Invocation, v map[string]any) ([]byte, error) {
	if ctx.Err() != nil || checkCandidateStorageBound(ctx) != nil {
		return o.failed(i.request, i.snapshot, i.claim, Canceled, "uncertain", i.active.receipt)
	}
	v["stateRevision"] = strconv.FormatUint(i.snapshot.StateRevision+2, 10)
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	if e = o.registry.ValidateResponse(raw, i.request); e != nil {
		return nil, e
	}
	desired, e := i.snapshot.Clone()
	if e != nil {
		return nil, e
	}
	j := desired.Journal[i.journalKey]
	if j.ResponseCommitID != "" {
		return o.failed(i.request, nil, i.claim, ReconciliationRequired, "uncertain", i.active.receipt)
	}
	j.ResponseCommitID = newID()
	j.PendingResponse = raw
	loaded, e := o.save(ctx, i.guard, desired)
	if e != nil {
		return o.failed(i.request, nil, i.claim, StorageFailure, "uncertain", i.active.receipt)
	}
	i.installSnapshot(loaded)
	desired, e = loaded.Clone()
	if e != nil {
		return nil, e
	}
	j = desired.Journal[i.journalKey]
	j.Response = raw
	j.Certainty = text(v, "certainty")
	j.Phase = "EXECUTE_RESULT"
	loaded, e = o.saveWithID(ctx, i.guard, desired, j.ResponseCommitID)
	if e != nil {
		return o.failed(i.request, nil, i.claim, StorageFailure, "uncertain", i.active.receipt)
	}
	i.installSnapshot(loaded)
	return raw, nil
}
func (o *Owner) verifyCompositionOutcomes(ctx context.Context, g *Guard, s *Snapshot) error {
	for _, j := range s.Journal {
		if j.Composition == nil || j.Composition.Stage != "done" {
			continue
		}
		if e := o.compositionContextError(ctx); e != nil {
			return e
		}
		r, e := o.backend.Reconcile(ctx, g, j.Composition.FinishID)
		stored, ok := s.Commits[j.Composition.FinishID]
		if e != nil || !ok || !committedOutcomeVisible(s, r) || !receiptMatches(r, stored) || !receiptPayloadMatches(j, j.Response, r, s.BindingID) || o.compositionContextError(ctx) != nil {
			return fail(ReconciliationRequired, "composition_final_readback")
		}
	}
	return nil
}

func (i *Invocation) remainingProviderBudget() (int, int64) {
	requests, remainingBytes := i.plan.MaxProviderRequests-i.effects, i.plan.MaxProviderBytes-i.bytes
	if i.compositionKey != "" {
		if root := i.snapshot.Journal[i.compositionKey]; root != nil && root.Composition != nil {
			used, reserved, e := compositionUsage(i.snapshot, i.compositionKey)
			if e != nil {
				return 0, 0
			}
			if n := root.Composition.Plan.MaxProviderRequests - used; n < requests {
				requests = n
			}
			if n := root.Composition.Plan.MaxProviderBytes - reserved; n < remainingBytes {
				remainingBytes = n
			}
		}
	}
	if requests < 0 {
		requests = 0
	}
	if remainingBytes < 0 {
		remainingBytes = 0
	}
	return requests, remainingBytes
}
