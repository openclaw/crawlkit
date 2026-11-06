package sessionowner

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Authorizer is a trusted host seam. Its peer comes from the actual authenticated
// transport, never a field in JSON. Confirmation and recovery approval are real
// owner checks; the core never interprets a supplied reference as permission.
type Authorizer interface {
	Authenticate(context.Context, any) (string, error)
	Allow(context.Context, string, Operation, map[string]any) error
	Confirm(context.Context, string, string, Operation, map[string]any) error
	Recovery(context.Context, string, string, *Request) error
}
type Channel struct {
	owner          *Owner
	subject, id    string
	mu             sync.Mutex
	closed         bool
	lifetime       context.Context
	cancelLifetime context.CancelFunc
}

func (o *Owner) AuthenticateChannel(ctx context.Context, peer any) (*Channel, error) {
	s, e := o.auth.Authenticate(ctx, peer)
	if e != nil || !validID(s) {
		return nil, fail(PermissionDenied, "authentication")
	}
	if ctx.Err() != nil {
		return nil, fail(Canceled, "authentication_lifetime")
	}
	lifetime, cancel := context.WithCancel(ctx)
	return &Channel{owner: o, subject: s, id: newID(), lifetime: lifetime, cancelLifetime: cancel}, nil
}
func (c *Channel) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancelLifetime()
	c.mu.Unlock()
}
func (c *Channel) check(o *Owner) error {
	if c == nil || c.owner != o {
		return fail(PermissionDenied, "channel")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.lifetime.Err() != nil {
		return fail(PermissionDenied, "channel_closed")
	}
	return nil
}

type AdapterOutput struct {
	CandidateTransferID string
	Result              map[string]any
	CredentialDelta     map[string]any
	Candidate           map[string]any
	Proof               map[string]any
	CandidateExpires    time.Time
	NoEffectEvidence    map[string]any
}
type Adapter interface {
	ArtifactID() string
	Execute(context.Context, *Invocation) (AdapterOutput, error)
}
type OperationRef struct {
	ID      string
	Version int
}

type OwnerOptions struct {
	SettlementAuthorization   ReadSettlementAuthorization
	ReadRecoveryOperations    []OperationRef
	ReadRecoveryAuthorization func(context.Context, string, *Request) (string, error)
	PrivateCandidates         map[string]CandidateBinding
	Registry                  *Registry
	Backend                   Backend
	Authorizer                Authorizer
	Adapters                  map[string]Adapter
	Clock                     func() time.Time
	WarmReady                 func() bool
	// Explicit owner qualification of existing registered safe reads. No public override.
	UncertainReadOperations []OperationRef
	// Proof must verify the exact previously enrolled actor/scope/purpose.
	Proof func(context.Context, *Snapshot, map[string]any, map[string]any) error
	// Delta validates auth-relevant response changes and coherent private context.
	Delta func(context.Context, *Snapshot, map[string]any) (map[string]any, error)
}
type activeOperation struct {
	composed                   bool
	mu                         sync.Mutex
	channel                    string
	channelRef                 *Channel
	cancel                     context.CancelFunc
	dispatched, done           bool
	certainty, receipt, digest string
	response                   []byte
}
type Owner struct {
	settlementAuthorization ReadSettlementAuthorization
	// Owner-local deterministic test boundary; nil in production.
	compositionBoundary       func(string)
	readRecovery              map[string]bool
	readRecoveryAuthorization func(context.Context, string, *Request) (string, error)
	candidateBindings         map[string]CandidateBinding
	registry                  *Registry
	backend                   Backend
	auth                      Authorizer
	adapters                  map[string]Adapter
	now                       func() time.Time
	warm                      func() bool
	proof                     func(context.Context, *Snapshot, map[string]any, map[string]any) error
	delta                     func(context.Context, *Snapshot, map[string]any) (map[string]any, error)
	mu                        sync.Mutex
	active                    map[string]*activeOperation
	uncertainReads            map[string]bool
	// Internal test seam only; nil retains the production context timer.
	deadlineHook func(context.Context, time.Time) (context.Context, context.CancelFunc)
}

func NewOwner(o OwnerOptions) (*Owner, error) {
	if o.Registry == nil || o.Backend == nil || o.Authorizer == nil {
		return nil, fail(ProtocolMismatch, "owner_options")
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.WarmReady == nil {
		o.WarmReady = func() bool { return false }
	}
	adapters := map[string]Adapter{}
	for id, a := range o.Adapters {
		b, ok := o.Registry.adapters[id]
		if !ok || a == nil || a.ArtifactID() != b.ArtifactSHA256 {
			return nil, fail(ProtocolMismatch, "adapter_artifact")
		}
		adapters[id] = a
	}
	candidates := map[string]CandidateBinding{}
	for id, b := range o.PrivateCandidates {
		a, ok := o.Registry.adapters[id]
		if !ok || a.CandidateSchemaID == nil || b.Expiry == nil {
			return nil, fail(ProtocolMismatch, "candidate_binding")
		}
		candidates[id] = b
	}
	exceptions := map[string]bool{}
	for _, ref := range o.UncertainReadOperations {
		key := operationKey(ref.ID, ref.Version, "perform")
		op, ok := o.Registry.operations[key]
		if !ok || op.Semantics != "safe_read" || op.ConfirmationRequired || exceptions[key] {
			return nil, fail(ProtocolMismatch, "uncertainty_exception")
		}
		exceptions[key] = true
	}
	composed, e := validateReadRecoveryOptions(o)
	if e != nil {
		return nil, e
	}
	return &Owner{settlementAuthorization: o.SettlementAuthorization, readRecovery: composed, readRecoveryAuthorization: o.ReadRecoveryAuthorization, candidateBindings: candidates, registry: o.Registry, backend: o.Backend, auth: o.Authorizer, adapters: adapters, now: o.Clock, warm: o.WarmReady, proof: o.Proof, delta: o.Delta, active: map[string]*activeOperation{}, uncertainReads: exceptions}, nil
}
func activeKey(c *Channel, id string) string  { return c.subject + "\x00" + c.id + "\x00" + id }
func journalKey(c *Channel, id string) string { return c.subject + "\x00" + id }

// Handle receives an already bounded payload on an authenticated channel. Framing
// uses registry.ChannelCap before allocation; this method applies operation caps.
func (o *Owner) Handle(ctx context.Context, c *Channel, raw []byte) ([]byte, error) {
	return o.handle(ctx, c, raw, false)
}

// HandleWithRecovery is an explicit trusted-owner opt-in, not a wire fallback.
func (o *Owner) HandleWithRecovery(ctx context.Context, c *Channel, raw []byte) ([]byte, error) {
	return o.handle(ctx, c, raw, true)
}
func (o *Owner) handle(ctx context.Context, c *Channel, raw []byte, composed bool) ([]byte, error) {
	if e := c.check(o); e != nil {
		return nil, e
	}
	authCtx, cancelAuth := context.WithCancel(ctx)
	stopChannel := context.AfterFunc(c.lifetime, cancelAuth)
	defer stopChannel()
	defer cancelAuth()
	ctx = authCtx
	req, e := o.registry.ParseRequest(raw)
	if e != nil {
		return nil, e
	}
	if composed && (text(req.Value, "kind") != "perform" || req.Operation.Semantics != "safe_read" || !o.readRecovery[operationKey(req.Operation.ID, req.Operation.Version, "perform")]) {
		return o.failed(req, nil, nil, PermissionDenied, "not_dispatched", newID())
	}
	if text(req.Value, "kind") == "cancel" {
		return o.cancel(c, req)
	}
	if e = o.auth.Allow(ctx, c.subject, req.Operation, req.Value); e != nil {
		return o.failed(req, nil, nil, PermissionDenied, "not_dispatched", newID())
	}
	if req.Operation.ConfirmationRequired {
		if e = o.auth.Confirm(ctx, c.subject, text(req.Value, "confirmationRef"), req.Operation, req.Value); e != nil {
			return o.failed(req, nil, nil, PermissionDenied, "not_dispatched", newID())
		}
	}
	if text(req.Value, "kind") == "recover" {
		if e = o.auth.Recovery(ctx, c.subject, text(req.Value, "recoveryAuthorizationRef"), req); e != nil {
			return o.failed(req, nil, nil, PermissionDenied, "not_dispatched", newID())
		}
	}
	// Current channel/schema/binding/enrollment and all real authorization above
	// take precedence. Only then may exact known history qualify a late refusal.
	if response, matched, cacheErr := o.refuseLateComposedCache(ctx, c, req); matched {
		return response, cacheErr
	}
	if !req.Deadline.After(o.now()) {
		certainty := "not_dispatched"
		if composed {
			certainty = "uncertain"
		}
		return o.failed(req, nil, nil, Code("deadline_exceeded"), certainty, newID())
	}
	remainingPhases := []string{"queue", "import", "startup", "capture", "proof", "action", "commit", "cleanup", "ipc"}
	if composed {
		remainingPhases = []string{"queue", "cleanup"}
	}
	plan, e := ReservePlan(req, o.now(), o.warmFor(req), remainingPhases)
	if e != nil {
		return o.failed(req, nil, nil, ErrorCode(e), "not_dispatched", newID())
	}
	deadline := req.Deadline
	if bound := o.now().Add(time.Duration(plan.MaxTotalMs) * time.Millisecond); bound.Before(deadline) {
		deadline = bound
	}
	runCtx, cancel := o.contextUntil(ctx, deadline)
	actualDeadline, _ := runCtx.Deadline()
	runCtx = context.WithValue(runCtx, operationBoundKey{}, operationBound{Deadline: actualDeadline, Cleanup: time.Duration(plan.PhaseMaxMs["commit"]+plan.PhaseMaxMs["cleanup"]) * time.Millisecond})
	defer cancel()
	key := activeKey(c, text(req.Value, "requestId"))
	entry := &activeOperation{composed: composed && plan.MaxSafeReadRetries == 1, channelRef: c, channel: c.id, cancel: cancel, receipt: newID(), certainty: "not_dispatched", digest: req.Digest}
	c.mu.Lock()
	if c.closed || c.lifetime.Err() != nil || ctx.Err() != nil {
		c.mu.Unlock()
		certainty := "not_dispatched"
		if composed {
			certainty = "uncertain"
		}
		return o.failed(req, nil, nil, Canceled, certainty, entry.receipt)
	}
	o.mu.Lock()
	if old := o.active[key]; old != nil {
		old.mu.Lock()
		if old.digest != req.Digest {
			old.mu.Unlock()
			o.mu.Unlock()
			c.mu.Unlock()
			return o.failed(req, nil, nil, StaleGeneration, "not_dispatched", entry.receipt)
		}
		if old.done && len(old.response) > 0 {
			answer := append([]byte(nil), old.response...)
			composedHistory := old.composed
			old.mu.Unlock()
			o.mu.Unlock()
			c.mu.Unlock()
			if composed || composedHistory {
				return o.deliverCompositionKnown(runCtx, req, answer, "cache_return")
			}
			return answer, nil
		}
		if !old.done || !composed {
			old.mu.Unlock()
			o.mu.Unlock()
			c.mu.Unlock()
			return o.failed(req, nil, nil, OwnerBusy, "not_dispatched", entry.receipt)
		}
		old.mu.Unlock()
		delete(o.active, key)
	}
	if len(o.active) >= 4096 {
		o.mu.Unlock()
		c.mu.Unlock()
		return o.failed(req, nil, nil, OwnerBusy, "not_dispatched", entry.receipt)
	}
	queued := 0
	for _, a := range o.active {
		a.mu.Lock()
		if !a.done {
			queued++
		}
		a.mu.Unlock()
	}
	if queued >= 16 {
		o.mu.Unlock()
		c.mu.Unlock()
		return o.failed(req, nil, nil, OwnerBusy, "not_dispatched", entry.receipt)
	}
	o.active[key] = entry
	o.mu.Unlock()
	c.mu.Unlock()
	defer func() { entry.mu.Lock(); entry.done = true; entry.mu.Unlock() }()
	queueCtx, queueCancel := context.WithTimeout(runCtx, time.Duration(plan.PhaseMaxMs["queue"])*time.Millisecond)
	defer queueCancel()
	guard, e := o.backend.Acquire(queueCtx)
	if e != nil {
		return o.failed(req, nil, nil, ErrorCode(e), "not_dispatched", entry.receipt)
	}
	defer guard.Close()
	s, e := o.backend.Load(runCtx, guard)
	if e != nil {
		return o.failed(req, nil, nil, ErrorCode(e), "not_dispatched", entry.receipt)
	}
	if s == nil || s.Validate() != nil {
		return o.failed(req, nil, nil, UnsafeStorage, "not_dispatched", entry.receipt)
	}
	if s.BindingID != o.registry.BindingID() {
		return o.failed(req, nil, nil, UnsafeStorage, "not_dispatched", entry.receipt)
	}
	settlementErr := o.verifyReadSettlements(runCtx, guard, s)
	if text(req.Value, "kind") == "inspect" {
		return o.inspect(req, s)
	}
	if settlementErr != nil {
		return o.failed(req, s, nil, ReconciliationRequired, "uncertain", entry.receipt)
	}
	if e = recoveryConsistency(s); e != nil {
		return o.failed(req, s, nil, ReconciliationRequired, "not_dispatched", entry.receipt)
	}
	jk := journalKey(c, text(req.Value, "requestId"))
	if previous := s.Journal[jk]; previous != nil {
		if previous.Digest != req.Digest {
			return o.failed(req, s, nil, StaleGeneration, "not_dispatched", entry.receipt)
		}
		if previous.Composition != nil {
			return o.readComposition(runCtx, guard, req, c.subject, s, jk)
		}
		if previous.UnresolvedIntentID != "" {
			return o.failed(req, s, s.Claims[previous.ClaimID], ReconciliationRequired, "uncertain", previous.ReceiptID)
		}
		if len(previous.Response) > 0 {
			if e = o.registry.ValidateResponse(previous.Response, req); e != nil {
				return nil, e
			}
			return append([]byte(nil), previous.Response...), nil
		}
		return o.failed(req, s, s.Claims[previous.ClaimID], ReconciliationRequired, previous.Certainty, previous.ReceiptID)
	}
	if e = o.verifyCompositionOutcomes(runCtx, guard, s); e != nil {
		return o.failed(req, s, nil, ReconciliationRequired, "uncertain", entry.receipt)
	}
	if text(req.Value, "kind") == "recover" && text(req.Value, "mode") == "resume" {
		if claim := s.Claims[text(req.Value, "claimId")]; claim != nil && claim.Phase == "ACCEPTED" {
			return o.acceptedReadback(runCtx, guard, req, c.subject, s, claim)
		}
	}
	if s.Generation == "" {
		return o.failed(req, s, nil, Code("enrollment_required"), "not_dispatched", entry.receipt)
	}
	if text(req.Value, "expectedGeneration") != s.Generation {
		return o.failed(req, s, nil, StaleGeneration, "not_dispatched", entry.receipt)
	}
	req.acceptedGeneration = s.Generation
	if s.Maintenance {
		return o.failed(req, s, nil, Code("maintenance_unavailable"), "not_dispatched", entry.receipt)
	}
	uncertain, recovering := durableCondition(s)
	if uncertain || recovering {
		qualifiedRead := o.uncertainReads[operationKey(req.Operation.ID, req.Operation.Version, req.Operation.RequestKind)]
		if !qualifiedRead && !o.exactResume(req, c.subject, s) {
			return o.failed(req, s, nil, ReconciliationRequired, "not_dispatched", entry.receipt)
		}
	}
	if len(s.Journal) >= 4096 {
		return o.failed(req, s, nil, StorageFailure, "not_dispatched", entry.receipt)
	}
	s.Journal[jk] = &JournalEntry{Generation: s.Generation, OperationID: req.Operation.ID, OperationVersion: req.Operation.Version, RequestKind: text(req.Value, "kind"), Caller: c.subject, RequestID: text(req.Value, "requestId"), Digest: req.Digest, Semantics: req.Operation.Semantics, Certainty: "not_dispatched", Phase: "EXECUTE_READY", ReceiptID: entry.receipt, Deadline: req.Deadline}
	if s, e = o.save(runCtx, guard, s); e != nil {
		return o.failed(req, nil, nil, StorageFailure, "uncertain", entry.receipt)
	}
	outerDeadline, _ := runCtx.Deadline()
	inv := &Invocation{operationCtx: runCtx, outerDeadline: outerDeadline, owner: o, request: req, plan: plan, guard: guard, snapshot: s, journalKey: jk, active: entry, step: "execute", Context: cloneMap(s.Credentials), Target: mapValue(req.Value["target"]), Args: mapValue(req.Value["args"])}
	if text(req.Value, "kind") == "recover" {
		return o.recover(runCtx, inv)
	}
	if composed && plan.MaxSafeReadRetries == 1 {
		return o.beginComposition(runCtx, inv)
	}
	return o.performInvocation(runCtx, inv)
}
func (o *Owner) performInvocation(runCtx context.Context, inv *Invocation) ([]byte, error) {
	req, s, entry := inv.request, inv.snapshot, inv.active
	if s.AuthBlocked {
		return o.finishFailure(runCtx, inv, Code("rejected_generation"), "not_dispatched", nil)
	}
	for key, t := range s.Fences {
		if key != "recovery" && t.After(o.now()) {
			return o.finishFailure(runCtx, inv, Code("throttled"), "not_dispatched", nil)
		}
	}
	output, e := o.execute(runCtx, inv)
	if inv.intentUncertain {
		return o.failed(req, nil, nil, ReconciliationRequired, "uncertain", entry.receipt)
	}
	if e != nil {
		certainty := "not_dispatched"
		if inv.dispatched() {
			certainty = "uncertain"
		}
		code := ErrorCode(e)
		if req.Operation.Semantics == "safe_read" && inv.dispatched() && runCtx.Err() == nil && o.now().Before(inv.Deadline()) && (code == "needs_login" || code == "rejected_generation") {
			cause := "rejected_generation"
			if code == "needs_login" {
				cause = "expired_session"
			}
			inv.snapshot.Journal[inv.journalKey].Diagnosis = &RejectedDiagnosis{ID: newID(), Epoch: req.acceptedGeneration, Cause: cause, Category: string(code), OperationID: req.Operation.ID, OperationVersion: req.Operation.Version, AdapterID: *req.Operation.AdapterID}
			certainty = "completed"
		}

		if runCtx.Err() != nil {
			code = Canceled
		}
		if certainty == "uncertain" && req.Operation.Semantics == "mutation" {
			code = UncertainWrite
		}
		return o.finishFailure(runCtx, inv, code, certainty, nil)
	}
	if !inv.dispatched() {
		return o.finishFailure(runCtx, inv, ProtocolMismatch, "not_dispatched", nil)
	}
	if e = o.applyDelta(runCtx, inv, output.CredentialDelta); e != nil {
		return o.finishFailure(runCtx, inv, ErrorCode(e), "uncertain", nil)
	}
	if e = o.registry.schemas.Validate(*req.Operation.ResultSchemaID, normalize(output.Result)); e != nil {
		return o.finishFailure(runCtx, inv, ProtocolMismatch, "uncertain", nil)
	}
	if containsPrivate(output.Result, s.Credentials) || containsPrivate(output.Result, output.CredentialDelta) {
		return o.finishFailure(runCtx, inv, PermissionDenied, "uncertain", nil)
	}
	result := o.baseOutcome(req, inv.snapshot, "completed", entry.receipt)
	result["kind"] = "performed"
	result["operationId"] = req.Operation.ID
	result["operationVersion"] = req.Operation.Version
	result["result"] = output.Result
	return o.finish(runCtx, inv, result)
}

func (o *Owner) warmFor(req *Request) bool {
	if text(req.Value, "kind") != "recover" {
		return false
	}
	return o.warm()
}

type Invocation struct {
	releaseContext                   context.CancelFunc
	parentActive                     *activeOperation
	compositionKey                   string
	operationCtx                     context.Context
	owner                            *Owner
	request                          *Request
	plan                             BudgetPlan
	guard                            *Guard
	snapshot                         *Snapshot
	journalKey                       string
	active                           *activeOperation
	step                             string
	Context, Target, Args, Candidate map[string]any
	claim                            *Claim
	effects                          int
	bytes                            int64
	effectiveDeadline                time.Time
	outerDeadline                    time.Time
	checkpointMu                     sync.Mutex
	intentUncertain                  bool
}

func (i *Invocation) Deadline() time.Time {
	if !i.effectiveDeadline.IsZero() {
		return i.effectiveDeadline
	}
	return i.request.Deadline
}
func (i *Invocation) Generation() string   { return i.snapshot.Generation }
func (i *Invocation) Operation() Operation { return cloneOperation(i.request.Operation) }

// Step is owner-selected progress, not a caller-supplied phase selector.
func (i *Invocation) Step() string { return i.step }

// RequestContext retains the native single-request ceiling within the already
// clamped phase/outer context. Adapters use it for each admitted request.
func (i *Invocation) RequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if i.compositionKey != "" {
		return i.compositionRequestContext(ctx)
	}
	if i.plan.MaxSingleProviderRequestMs == nil {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(*i.plan.MaxSingleProviderRequestMs)*time.Millisecond)
}
func (i *Invocation) dispatched() bool {
	i.active.mu.Lock()
	defer i.active.mu.Unlock()
	return i.active.dispatched
}

// BeforeEffect durably records intent before returning permission. Native
// adapters and FD checkpoint ACKs must call it for EVERY external effect.
func (i *Invocation) BeforeEffect(ctx context.Context, phase string) error {
	i.checkpointMu.Lock()
	defer i.checkpointMu.Unlock()
	if i.intentUncertain {
		return fail(ReconciliationRequired, "unresolved_intent")
	}
	c := i.active.channelRef
	if err := c.check(i.owner); err != nil {
		return fail(Canceled, "channel_closed")
	}
	if ctx.Err() != nil {
		return fail(Canceled, "before_effect")
	}
	allowed := map[string]string{"execute": "EXECUTING", "readiness": "WAIT_READINESS", "acquire_candidate": "ACQUIRING", "prove_candidate": "PROVING", "reconcile": "RECONCILING"}
	if allowed[i.step] != phase {
		return fail(ProtocolMismatch, "effect_phase")
	}
	if e := i.checkCompositionPermit(); e != nil {
		return e
	}
	if i.effects >= i.plan.MaxProviderRequests {
		return fail(BudgetInsufficient, "requests")
	}
	remaining := i.request.Deadline.Sub(i.owner.now())
	if d, ok := ctx.Deadline(); ok && d.Sub(i.owner.now()) < remaining {
		remaining = d.Sub(i.owner.now())
	}
	reserve := i.plan.PhaseMaxMs["cleanup"] + i.plan.PhaseMaxMs["commit"]
	remainingPhases := []string{"action", "ipc"}
	switch i.step {
	case "readiness":
		remainingPhases = []string{"startup", "capture", "proof", "ipc"}
	case "acquire_candidate":
		remainingPhases = []string{"capture", "proof", "ipc"}
	case "prove_candidate":
		remainingPhases = []string{"proof", "ipc"}
	}
	for _, p := range remainingPhases {
		reserve += phaseReserve(p, i.plan.PhaseMaxMs[p])
	}
	// ctx is phase-clamped below; outerDeadline still owns future phases. Check
	// remaining phase reserves against the original operation deadline, not a fresh
	// timer or a caller-supplied phase. Completed phases never reappear here.
	outerDeadline := i.outerDeadline
	if outerDeadline.IsZero() || i.request.Deadline.Before(outerDeadline) {
		outerDeadline = i.request.Deadline
	}
	outer := outerDeadline.Sub(i.owner.now())
	if remaining <= 0 || outer < time.Duration(reserve)*time.Millisecond {
		return fail(BudgetInsufficient, "effect_reserve")
	}
	// Preparation is itself a positively verified durable transaction. A crash
	// before the actual intent attempt leaves its exact identity discoverable.
	intentID, effectID := newID(), newID()
	prepared, cloneErr := i.snapshot.Clone()
	if cloneErr != nil {
		return fail(StorageFailure, "intent_prepare")
	}
	pj := prepared.Journal[i.journalKey]
	if pj.Intents == nil {
		pj.Intents = map[string]IntentReservation{}
	}
	for _, existing := range pj.Intents {
		if existing.State == "prepared" {
			return fail(ReconciliationRequired, "pending_intent")
		}
	}
	claimID := ""
	if i.claim != nil {
		claimID = i.claim.ID
	}
	beforeAcq, beforeProof := 0, 0
	if i.claim != nil {
		beforeAcq = i.claim.AcquisitionAttempts
		beforeProof = i.claim.ProofAttempts
	}
	pj.Intents[intentID] = IntentReservation{BeforeDispatches: pj.Dispatches, BeforeAcquisitionAttempts: beforeAcq, BeforeProofAttempts: beforeProof, ID: intentID, State: "prepared", BindingID: prepared.BindingID, Caller: pj.Caller, RequestID: pj.RequestID, RequestDigest: pj.Digest, OperationID: pj.OperationID, OperationVersion: pj.OperationVersion, Epoch: prepared.Generation, ClaimID: claimID, Step: i.step, Phase: phase, EffectID: effectID}
	loaded, e := i.owner.save(ctx, i.guard, prepared)
	if e != nil {
		// No actual intent transaction has been attempted. Never turn uncertain
		// preparation into permission or hide a possibly persisted preparation.
		var outcome *intentCommitError
		if errors.As(e, &outcome) {
			if outcome.notCommitted && outcome.authoritative != nil {
				i.installSnapshot(outcome.authoritative)
			} else {
				i.intentUncertain = true
			}
		}
		return e
	}
	i.installSnapshot(loaded)
	if ctx.Err() != nil {
		return fail(Canceled, "prepared_before_attempt")
	}
	staged, cloneErr := i.snapshot.Clone()
	if cloneErr != nil {
		return fail(StorageFailure, "intent_stage")
	}
	stage := &Invocation{owner: i.owner, request: i.request, plan: i.plan, snapshot: staged, journalKey: i.journalKey, step: i.step}
	if i.claim != nil {
		stage.claim = staged.Claims[i.claim.ID]
	}
	if stage.claim != nil {
		if err := stage.consumeClaimPermit(effectID); err != nil {
			return err
		}
	}
	journal := staged.Journal[i.journalKey]
	journal.EffectIDs = append(journal.EffectIDs, effectID)
	journal.ResolutionID = ""
	journal.Certainty = "uncertain"
	journal.Phase = phase
	journal.Dispatches++
	if i.compositionKey != "" {
		journal.ProviderBytesReserved += int64(i.request.Operation.WireCaps.MaxProviderBodyBytes)
	}
	record := journal.Intents[intentID]
	record.State = "committed"
	journal.Intents[intentID] = record
	loaded, e = i.owner.saveWithID(ctx, i.guard, staged, intentID)
	if e != nil {
		var outcome *intentCommitError
		if errors.As(e, &outcome) && outcome.notCommitted && outcome.authoritative != nil {
			i.installSnapshot(outcome.authoritative)
			// This is a proved disposition, not a refunded permit. If cleanup fails,
			// durable preparation still forces exact readback on the next resume.
			r := i.snapshot.Journal[i.journalKey].Intents[intentID]
			r.State = "not_committed"
			i.snapshot.Journal[i.journalKey].Intents[intentID] = r
		} else {
			i.intentUncertain = true
		}
		return e
	}
	i.installSnapshot(loaded)
	c.mu.Lock()
	defer c.mu.Unlock()
	i.active.mu.Lock()
	defer i.active.mu.Unlock()
	if ctx.Err() != nil || c.closed || c.lifetime.Err() != nil {
		return fail(Canceled, "before_effect_lifetime")
	}
	i.effects++
	i.active.dispatched = true
	i.active.certainty = "uncertain"
	if i.parentActive != nil && i.parentActive != i.active {
		i.parentActive.mu.Lock()
		i.parentActive.dispatched = true
		i.parentActive.certainty = "uncertain"
		i.parentActive.mu.Unlock()
	}
	return nil
}
func (i *Invocation) installSnapshot(s *Snapshot) {
	id := ""
	if i.claim != nil {
		id = i.claim.ID
	}
	i.snapshot = s
	if id != "" {
		i.claim = s.Claims[id]
	}
}

func (i *Invocation) AcceptProviderBody(b []byte) error {
	if i.compositionKey != "" {
		j := i.snapshot.Journal[i.journalKey]
		if j == nil || i.bytes+int64(len(b)) > j.ProviderBytesReserved {
			return fail(PayloadTooLarge, "composition_body_reservation")
		}
	}
	if e := CheckBytes(b, i.request.Operation.WireCaps.MaxProviderBodyBytes); e != nil {
		return e
	}
	if i.bytes+int64(len(b)) > i.plan.MaxProviderBytes {
		return fail(PayloadTooLarge, "total_bytes")
	}
	i.bytes += int64(len(b))
	return nil
}
func (i *Invocation) AcceptCursor(s string) error {
	return CheckBytes([]byte(s), i.request.Operation.WireCaps.MaxCursorBytes)
}

func (o *Owner) execute(ctx context.Context, i *Invocation) (AdapterOutput, error) {
	if i.request.Operation.AdapterID == nil {
		return AdapterOutput{}, fail(ProtocolMismatch, "adapter")
	}
	id := *i.request.Operation.AdapterID
	a := o.adapters[id]
	binding := o.registry.adapters[id]
	if a == nil || a.ArtifactID() != binding.ArtifactSHA256 {
		return AdapterOutput{}, fail(ProtocolMismatch, "adapter_artifact")
	}
	raw, _ := json.Marshal(i.Context)
	if e := CheckBytes(raw, i.request.Operation.WireCaps.MaxPrivateContextBytes); e != nil {
		return AdapterOutput{}, e
	}
	if e := o.registry.schemas.Validate(binding.PrivateContextSchemaID, normalize(i.Context)); e != nil {
		return AdapterOutput{}, e
	}
	return o.runPhase(ctx, i, a.Execute)
}
func (o *Owner) runPhase(ctx context.Context, i *Invocation, call func(context.Context, *Invocation) (AdapterOutput, error)) (AdapterOutput, error) {
	phase := "action"
	switch i.step {
	case "readiness":
		phase = "startup"
	case "acquire_candidate":
		phase = "capture"
	case "prove_candidate":
		phase = "proof"
	}
	deadline := i.request.Deadline
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	deadline = deadline.Add(-time.Duration(i.plan.PhaseMaxMs["cleanup"]+i.plan.PhaseMaxMs["commit"]+i.plan.PhaseMaxMs["ipc"]) * time.Millisecond)
	if ceiling := i.plan.PhaseMaxMs[phase]; ceiling > 0 {
		d := o.now().Add(time.Duration(ceiling) * time.Millisecond)
		if d.Before(deadline) {
			deadline = d
		}
	}
	if !deadline.After(o.now()) {
		return AdapterOutput{}, fail(BudgetInsufficient, "phase_deadline")
	}
	if i.compositionKey != "" && i.snapshot.Journal[i.compositionKey].Composition.Stage == "initial" {
		future := compositionRecoveryReserve(i.snapshot.Journal[i.compositionKey].Composition.Plan)
		bound := i.snapshot.Journal[i.compositionKey].Composition.Deadline.Add(-future)
		if bound.Before(deadline) {
			deadline = bound
		}
		if !deadline.After(o.now()) {
			return AdapterOutput{}, fail(BudgetInsufficient, "composition_future_reserve")
		}
	}
	i.effectiveDeadline = deadline
	phaseCtx, cancel := o.contextUntil(ctx, deadline)
	defer cancel()
	out, err := call(phaseCtx, i)
	if i.compositionKey != "" && (phaseCtx.Err() != nil || !o.now().Before(deadline)) {
		return out, fail(Code("deadline_exceeded"), "composition_phase")
	}
	if i.intentUncertain {
		return out, fail(ReconciliationRequired, "unresolved_intent")
	}
	return out, err
}
func (o *Owner) applyDelta(ctx context.Context, i *Invocation, d map[string]any) error {
	if d == nil {
		return nil
	}
	b := o.registry.adapters[*i.request.Operation.AdapterID]
	if b.CredentialDeltaSchemaID == nil || o.delta == nil {
		return fail(ProtocolMismatch, "delta_unqualified")
	}
	raw, _ := json.Marshal(d)
	if e := CheckBytes(raw, i.request.Operation.WireCaps.MaxCredentialDeltaBytes); e != nil {
		return e
	}
	if e := o.registry.schemas.Validate(*b.CredentialDeltaSchemaID, normalize(d)); e != nil {
		return e
	}
	n, e := o.delta(ctx, i.snapshot, d)
	if e != nil {
		return e
	}
	rev, e := nextRevision(i.snapshot.CredentialRevision)
	if e != nil {
		return e
	}
	i.snapshot.Credentials = cloneMap(n)
	i.snapshot.CredentialRevision = rev
	i.snapshot, e = o.save(ctx, i.guard, i.snapshot)
	return e
}
func (o *Owner) finish(ctx context.Context, i *Invocation, v map[string]any) ([]byte, error) {
	if i.compositionKey != "" {
		return o.finishCompositionChild(ctx, i, v)
	}
	// Record the response at the revision produced by this final atomic commit.
	rev, e := nextRevision(i.snapshot.StateRevision)
	if e != nil {
		return nil, e
	}
	v["stateRevision"] = strconv.FormatUint(rev, 10)
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	if e = o.registry.ValidateResponse(raw, i.request); e != nil {
		return nil, e
	}
	j := i.snapshot.Journal[i.journalKey]
	j.Response = raw
	j.Certainty = text(v, "certainty")
	j.Phase = "EXECUTE_RESULT"
	i.snapshot, e = o.save(context.WithoutCancel(ctx), i.guard, i.snapshot)
	if e != nil {
		return o.failed(i.request, nil, nil, StorageFailure, "uncertain", i.active.receipt)
	}
	i.active.mu.Lock()
	i.active.certainty = j.Certainty
	i.active.response = append([]byte(nil), raw...)
	i.active.mu.Unlock()
	return raw, nil
}
func (o *Owner) finishFailure(ctx context.Context, i *Invocation, c Code, certainty string, claim *Claim) ([]byte, error) {
	if j := i.snapshot.Journal[i.journalKey]; j != nil && j.Diagnosis != nil {
		i.snapshot.AuthBlocked = true
	}
	v := o.failureMap(i.request, i.snapshot, claim, c, certainty, i.active.receipt)
	return o.finish(ctx, i, v)
}
func (o *Owner) baseOutcome(req *Request, s *Snapshot, certainty, receipt string) map[string]any {
	var gen, rev any
	if s != nil && s.Generation != "" {
		gen = s.Generation
		rev = strconv.FormatUint(s.StateRevision, 10)
	}
	var accepted any
	if req.acceptedGeneration != "" {
		accepted = req.acceptedGeneration
	}
	return map[string]any{"protocol": Protocol, "requestId": text(req.Value, "requestId"), "bindingId": o.registry.BindingID(), "acceptedGeneration": accepted, "finalGeneration": gen, "stateRevision": rev, "certainty": certainty, "receiptRef": receipt, "nextAction": "none", "retryNotBefore": nil}
}
func (o *Owner) failureMap(req *Request, s *Snapshot, claim *Claim, c Code, certainty, receipt string) map[string]any {
	v := o.baseOutcome(req, s, certainty, receipt)
	action := "operator_review"
	switch c {
	case BudgetInsufficient:
		action = "supply_budget"
	case Canceled:
		action = "none"
	case OwnerBusy:
		action = "wait_owner"
	case ReconciliationRequired, UncertainWrite:
		action = "reconcile"
	case "enrollment_required":
		action = "needs_enrollment"
	case "needs_login", "rejected_generation":
		action = "sign_in_required"
	case "maintenance_unavailable":
		action = "wait_maintenance"
	case "throttled":
		action = "retry_after"
	}
	var retry any
	if s != nil {
		var latest time.Time
		for _, t := range s.Fences {
			if t.After(latest) && t.After(o.now()) {
				latest = t
			}
		}
		if !latest.IsZero() {
			retry = timestamp(latest)
		}
	}
	v["kind"] = "failed"
	v["requestKind"] = text(req.Value, "kind")
	v["nextAction"] = action
	v["retryNotBefore"] = retry
	v["failure"] = map[string]any{"category": string(c), "retryable": false, "certainty": certainty, "nextAction": action, "retryNotBefore": retry, "detailCode": nil}
	v["claim"] = claimStatus(claim)
	return v
}
func (o *Owner) failed(req *Request, s *Snapshot, c *Claim, code Code, certainty, receipt string) ([]byte, error) {
	v := o.failureMap(req, s, c, code, certainty, receipt)
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	return b, o.registry.ValidateResponse(b, req)
}
func (o *Owner) inspect(req *Request, s *Snapshot) ([]byte, error) {
	var gen, rev, identity any
	state := "unenrolled"
	if s.Generation != "" {
		gen = s.Generation
		rev = strconv.FormatUint(s.StateRevision, 10)
		identity = o.registry.policy.SafeIdentity
		state = "ready"
	}
	if s.AuthBlocked {
		state = "auth_blocked"
	}
	if s.Maintenance {
		state = "maintenance"
	}
	nextAction := "none"
	uncertain, recovering := durableCondition(s)
	if recovering {
		state = "recovering"
		nextAction = "resume_claim"
	}
	if uncertain {
		state = "uncertain"
		nextAction = "reconcile"
	}
	v := map[string]any{"protocol": Protocol, "kind": "inspection", "requestId": text(req.Value, "requestId"), "bindingId": o.registry.BindingID(), "authorityMode": o.registry.policy.AuthorityMode, "state": state, "generation": gen, "stateRevision": rev, "safeIdentity": identity, "lastFailure": nil, "pendingDeadline": nil, "retryNotBefore": nil, "nextAction": nextAction}
	b, _ := json.Marshal(v)
	return b, o.registry.ValidateResponse(b, req)
}
func (o *Owner) cancel(c *Channel, req *Request) ([]byte, error) {
	o.mu.Lock()
	a := o.active[activeKey(c, text(req.Value, "targetRequestId"))]
	o.mu.Unlock()
	v := map[string]any{"protocol": Protocol, "kind": "cancel_ack", "requestId": text(req.Value, "requestId"), "bindingId": o.registry.BindingID(), "targetRequestId": text(req.Value, "targetRequestId"), "disposition": "not_found", "certainty": nil, "outcomeRef": nil}
	if a != nil {
		a.mu.Lock()
		if a.done {
			v["disposition"] = "already_terminal"
			v["certainty"] = a.certainty
			v["outcomeRef"] = a.receipt
		} else {
			a.cancel()
			if a.dispatched {
				v["disposition"] = "cancellation_requested"
				v["certainty"] = "uncertain"
			} else {
				v["disposition"] = "withdrawn_before_effect"
				v["certainty"] = "not_dispatched"
			}
		}
		a.mu.Unlock()
	}
	b, _ := json.Marshal(v)
	return b, o.registry.ValidateResponse(b, req)
}
func normalize(v any) any {
	b, e := json.Marshal(v)
	if e != nil {
		return nil
	}
	x, e := DecodeJSON(b, HardFrameLimit)
	if e != nil {
		return nil
	}
	return x
}
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	x, _ := object(normalize(m))
	return x
}
func mapValue(v any) map[string]any {
	if m, ok := object(v); ok {
		return cloneMap(m)
	}
	return map[string]any{}
}
func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func claimStatus(c *Claim) any {
	if c == nil {
		return nil
	}
	return map[string]any{"claimId": c.ID, "phase": c.Phase, "readinessResumptions": c.ReadinessResumptions, "acquisitionAttempts": c.AcquisitionAttempts, "proofAttempts": c.ProofAttempts}
}
func containsPrivate(v, private any) bool {
	secrets := []string{}
	var collect func(any)
	collect = func(x any) {
		switch x := x.(type) {
		case string:
			if x != "" {
				secrets = append(secrets, x)
			}
		case map[string]any:
			for _, v := range x {
				collect(v)
			}
		case []any:
			for _, v := range x {
				collect(v)
			}
		}
	}
	collect(private)
	found := false
	var scan func(any)
	scan = func(x any) {
		switch x := x.(type) {
		case string:
			for _, s := range secrets {
				if strings.Contains(x, s) {
					found = true
				}
			}
		case map[string]any:
			for k, v := range x {
				scan(k)
				scan(v)
			}
		case []any:
			for _, v := range x {
				scan(v)
			}
		}
	}
	scan(v)
	return found
}
