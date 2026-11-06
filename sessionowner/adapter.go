package sessionowner

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// AdapterSession is the trusted, private FD protocol. It is never reachable by
// public Inspect/Perform/Recover payloads. Incoming lease JSON is compared with
// an independently constructed owner lease, not treated as authorization.
type AdapterSession struct {
	mu                                                 sync.Mutex
	invocation                                         *Invocation
	lease                                              map[string]any
	inSeq, outSeq                                      uint64
	terminal, invoked, consumeAttempted, deliveryReady bool
	parentContext                                      context.Context
	cancelParent                                       context.CancelFunc
	permitted                                          atomic.Bool
	outboundKey                                        string
	noEffect                                           map[string]any
	phase                                              string
}

func NewAdapterSession(i *Invocation, parentLifetime string) (*AdapterSession, error) {
	if !validID(parentLifetime) || i.request.Operation.AdapterID == nil {
		return nil, fail(ProtocolMismatch, "adapter_lifetime")
	}
	b := i.owner.registry.adapters[*i.request.Operation.AdapterID]
	rpcCtx, stop := i.RPCContext(context.Background())
	rpcDeadline, _ := rpcCtx.Deadline()
	stop()
	var claim any
	if i.claim != nil {
		claim = i.claim.ID
	}
	lease := map[string]any{"leaseId": newID(), "invocationId": newID(), "requestId": text(i.request.Value, "requestId"), "bindingId": i.owner.registry.BindingID(), "adapterId": b.ID, "adapterArtifactSha256": b.ArtifactSHA256, "operationId": i.request.Operation.ID, "operationVersion": i.request.Operation.Version, "operationDigest": i.request.Digest, "authorityGeneration": i.snapshot.Generation, "baseStateRevision": strconv.FormatUint(i.snapshot.StateRevision, 10), "baseCredentialRevision": strconv.FormatUint(i.snapshot.CredentialRevision, 10), "claimId": claim, "parentLifetimeId": parentLifetime, "deadline": timestamp(rpcDeadline)}
	if e := i.owner.registry.schemas.Validate(schemaPrefix+"adapter#/$defs/Lease", normalize(lease)); e != nil {
		return nil, e
	}
	parent := i.operationCtx
	if parent == nil {
		parent = context.Background()
	}
	lifetime, cancel := context.WithCancel(parent)
	return &AdapterSession{invocation: i, lease: lease, parentContext: lifetime, cancelParent: cancel}, nil
}
func (s *AdapterSession) envelope(kind string) map[string]any {
	return map[string]any{"protocol": AdapterProtocol, "kind": kind, "direction": "authority_to_adapter", "lease": s.lease, "sequence": strconv.FormatUint(s.outSeq, 10)}
}
func (s *AdapterSession) InvokeFrame() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outSeq != 0 || s.invoked || s.parentContext.Err() != nil {
		return nil, fail(ProtocolMismatch, "invoke_replay")
	}
	s.invoked = true // An uncertain staged publication cannot be retried in memory.
	i := s.invocation
	leaseDeadline, err := time.Parse("2006-01-02T15:04:05.000Z", text(s.lease, "deadline"))
	if err != nil || !i.owner.now().Before(leaseDeadline) {
		return nil, fail(Code("deadline_exceeded"), "adapter_deadline")
	}
	if !equalJSON(normalize(i.Context), normalize(i.snapshot.Credentials)) || !equalJSON(normalize(i.Target), normalize(mapValue(i.request.Value["target"]))) || !equalJSON(normalize(i.Args), normalize(mapValue(i.request.Value["args"]))) {
		return nil, fail(ProtocolMismatch, "invocation_snapshot")
	}
	if i.Candidate != nil && (i.claim == nil || !equalJSON(normalize(i.Candidate), normalize(i.claim.Candidate))) {
		return nil, fail(ProtocolMismatch, "candidate_snapshot")
	}
	v := s.envelope("invoke")
	v["step"] = i.step
	v["target"] = i.Target
	v["args"] = i.Args
	v["privateContext"] = i.Context
	v["candidate"] = nil
	if i.Candidate != nil {
		v["candidate"] = map[string]any{"mode": "inline", "value": i.Candidate}
	}
	remainingRequests, remainingBytes := i.remainingProviderBudget()
	v["budget"] = map[string]any{"phaseDeadline": text(s.lease, "deadline"), "cleanupReserveMs": i.plan.PhaseMaxMs["cleanup"], "remainingProviderRequests": remainingRequests, "remainingProviderBytes": remainingBytes}
	b, e := s.encode(v)
	if e != nil && ErrorCode(e) == PayloadTooLarge && i.Candidate != nil {
		base := i.operationCtx
		if base == nil {
			base = context.Background()
		}
		ctx, cancel, check, boundErr := s.candidateHandoffContext(base, i.claim.CandidateExpires)
		if boundErr != nil {
			return nil, boundErr
		}
		defer cancel()
		b, e = s.outboundCandidate(ctx, v)
		if e == nil {
			e = check()
		}
	}
	if e == nil && (s.parentContext.Err() != nil || !i.owner.now().Before(leaseDeadline)) {
		e = fail(Canceled, "invoke_lifetime")
	}
	if e != nil {
		return nil, e
	}
	s.outSeq++
	return b, nil
}
func (s *AdapterSession) encode(v map[string]any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	if e = CheckBytes(b, s.invocation.request.Operation.WireCaps.AdapterFrameBytes); e != nil {
		return nil, e
	}
	x, e := DecodeJSON(b, s.invocation.request.Operation.WireCaps.AdapterFrameBytes)
	if e != nil {
		return nil, e
	}
	return b, s.invocation.owner.registry.schemas.Validate(schemaPrefix+"adapter", x)
}

var checkpointEvents = map[string]map[string]map[string]bool{
	"execute":           {"before_effect": {"EXECUTING": true}, "after_effect": {"EXECUTE_RESULT": true}, "paused_no_effect": {"EXECUTE_READY": true}},
	"readiness":         {"before_effect": {"WAIT_READINESS": true}, "after_effect": {"WAIT_READINESS": true}, "paused_no_effect": {"WAIT_READINESS": true}},
	"acquire_candidate": {"before_effect": {"ACQUIRING": true}, "candidate_staged": {"CANDIDATE_READY": true}, "after_effect": {"CANDIDATE_READY": true}},
	"prove_candidate":   {"before_effect": {"PROVING": true}, "proof_accepted": {"PROVED": true}, "proof_rejected": {"REJECTED": true}},
	"reconcile":         {"before_effect": {"RECONCILING": true}, "after_effect": {"EXECUTE_RESULT": true}},
}

// Accept consumes exactly one child frame. An ACK is returned only after the
// journal/claim checkpoint is durable. The caller must not synthesize an ACK.
func (s *AdapterSession) Accept(ctx context.Context, raw []byte) (ack []byte, result *AdapterOutput, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.invocation
	leaseDeadline, deadlineErr := time.Parse("2006-01-02T15:04:05.000Z", text(s.lease, "deadline"))
	if deadlineErr != nil || !i.owner.now().Before(leaseDeadline) {
		return nil, nil, fail(Code("deadline_exceeded"), "adapter_deadline")
	}
	ctx, cancel := i.owner.contextUntil(ctx, leaseDeadline)
	defer cancel()
	if s.parentContext.Err() != nil || s.terminal || ctx.Err() != nil {
		return nil, nil, fail(Canceled, "adapter_lifetime")
	}
	v, e := DecodeJSON(raw, i.request.Operation.WireCaps.AdapterFrameBytes)
	if e != nil {
		return nil, nil, e
	}
	if e = i.owner.registry.schemas.Validate(schemaPrefix+"adapter", v); e != nil {
		return nil, nil, e
	}
	m, _ := object(v)
	if text(m, "direction") != "adapter_to_authority" || !equalJSON(normalize(s.lease), m["lease"]) || text(m, "sequence") != strconv.FormatUint(s.inSeq, 10) {
		return nil, nil, fail(ProtocolMismatch, "lease_sequence")
	}
	if s.inSeq == ^uint64(0) {
		return nil, nil, fail(ProtocolMismatch, "sequence_overflow")
	}
	s.inSeq++
	binding := i.owner.registry.adapters[*i.request.Operation.AdapterID]
	switch text(m, "kind") {
	case "checkpoint":
		event, phase := text(m, "event"), text(m, "phase")
		if !checkpointEvents[i.step][event][phase] {
			return nil, nil, fail(ProtocolMismatch, "checkpoint_transition")
		}
		if i.step != "execute" && i.step != "reconcile" && i.claim == nil {
			return nil, nil, fail(ProtocolMismatch, "checkpoint_claim")
		}
		if e = i.owner.registry.schemas.Validate(binding.CheckpointEvidenceSchemaID, m["evidence"]); e != nil {
			return nil, nil, e
		}
		if event == "before_effect" {
			if s.outboundKey != "" && !s.deliveryReady {
				return nil, nil, fail(PermissionDenied, "candidate_not_delivered")
			}
			if text(m, "effectCertainty") != "not_dispatched" {
				return nil, nil, fail(ProtocolMismatch, "effect_certainty")
			}
			if e = i.BeforeEffect(ctx, phase); e != nil {
				return nil, nil, e
			}
			s.permitted.Store(true)
			if s.parentContext.Err() != nil || ctx.Err() != nil {
				return nil, nil, fail(Canceled, "adapter_lifetime")
			}
			s.noEffect = nil
		} else if event == "paused_no_effect" {
			if s.permitted.Load() || i.dispatched() || text(m, "effectCertainty") != "not_dispatched" {
				return nil, nil, fail(ReconciliationRequired, "no_effect")
			}
			s.noEffect = mapValue(m["evidence"])
			// Even a no-effect readiness checkpoint is persisted before ACK.
			i.snapshot.Journal[i.journalKey].Phase = phase
			i.snapshot, e = i.owner.save(ctx, i.guard, i.snapshot)
			if e != nil {
				return nil, nil, e
			}
		} else {
			if !s.permitted.Load() || text(m, "effectCertainty") == "not_dispatched" {
				return nil, nil, fail(ProtocolMismatch, "after_effect")
			}
			i.snapshot.Journal[i.journalKey].Phase = phase
			i.snapshot, e = i.owner.save(ctx, i.guard, i.snapshot)
			if e != nil {
				return nil, nil, e
			}
		}
		s.phase = phase
		reply := s.envelope("checkpoint_ack")
		reply["checkpointSequence"] = text(m, "sequence")
		reply["durableStateRevision"] = strconv.FormatUint(i.snapshot.StateRevision, 10)
		reply["authorizedToProceed"] = true
		b, e := s.encode(reply)
		if e == nil {
			s.outSeq++
		}
		return b, nil, e
	case "adapter_result":
		out := &AdapterOutput{Result: mapValue(m["result"]), NoEffectEvidence: s.noEffect}
		if m["credentialDelta"] != nil {
			// Checkpoints advance journal state, not the leased credential base.
			// A same-guard owner rotation must not be overwritten by an older
			// private context even when every echoed lease field still matches.
			if text(s.lease, "authorityGeneration") != i.snapshot.Generation || text(s.lease, "baseCredentialRevision") != strconv.FormatUint(i.snapshot.CredentialRevision, 10) {
				return nil, nil, fail(StaleGeneration, "credential_base")
			}
			if binding.CredentialDeltaSchemaID == nil {
				return nil, nil, fail(ProtocolMismatch, "delta")
			}
			if e = i.owner.registry.schemas.Validate(*binding.CredentialDeltaSchemaID, m["credentialDelta"]); e != nil {
				return nil, nil, e
			}
			b, _ := json.Marshal(m["credentialDelta"])
			if e = CheckBytes(b, i.request.Operation.WireCaps.MaxCredentialDeltaBytes); e != nil {
				return nil, nil, e
			}
			out.CredentialDelta = mapValue(m["credentialDelta"])
		}
		if m["candidate"] != nil {
			if text(m, "status") != "ok" {
				return nil, nil, fail(ProtocolMismatch, "candidate_failure_frame")
			}
			wire, _ := object(m["candidate"])
			transfer, e := s.receiveCandidate(ctx, wire)
			if e != nil {
				return nil, nil, e
			}
			out.Candidate = cloneMap(transfer.Candidate)
			out.CandidateExpires = transfer.ExpiresAt
			out.CandidateTransferID = transfer.Key
		}
		if m["proof"] != nil {
			if binding.ProofSchemaID == nil {
				return nil, nil, fail(ProtocolMismatch, "proof")
			}
			if e = i.owner.registry.schemas.Validate(*binding.ProofSchemaID, m["proof"]); e != nil {
				return nil, nil, e
			}
			out.Proof = mapValue(m["proof"])
		}
		if text(m, "status") == "failed" {
			f, _ := object(m["failure"])
			if text(f, "certainty") != text(m, "certainty") {
				return nil, nil, fail(ProtocolMismatch, "result_certainty")
			}
			s.terminal = true
			return nil, out, fail(Code(text(f, "category")), "adapter_failure")
		}
		if !s.permitted.Load() && s.noEffect == nil {
			return nil, nil, fail(ProtocolMismatch, "missing_checkpoint")
		}
		if i.step == "execute" {
			if e = i.owner.registry.schemas.Validate(*i.request.Operation.ResultSchemaID, m["result"]); e != nil {
				return nil, nil, e
			}
		}
		s.terminal = true
		return nil, out, nil
	default:
		return nil, nil, fail(ProtocolMismatch, "unexpected_adapter_frame")
	}
}
func (s *AdapterSession) ParentLost() string {
	// Cancellation must not wait for a repository/storage callback holding mu,
	// including callbacks that report parent loss synchronously on this goroutine.
	s.cancelParent()
	if s.permitted.Load() {
		return "uncertain"
	}
	return "not_dispatched"
}

// RunPrivateFD binds an already-owned duplex descriptor to one invocation. It
// starts no process, listener, browser or SDK. Host code owns child supervision.
// Closing ctx must close the descriptor, so a blocked read cannot outlive parent.
func RunPrivateFD(ctx context.Context, session *AdapterSession, fd io.ReadWriteCloser) (AdapterOutput, error) {
	deadline, err := time.Parse("2006-01-02T15:04:05.000Z", text(session.lease, "deadline"))
	if err != nil {
		fd.Close()
		return AdapterOutput{}, fail(ProtocolMismatch, "private_deadline")
	}
	ctx, cancel := session.invocation.owner.contextUntil(ctx, deadline)
	defer cancel()
	if ctx.Err() != nil {
		session.ParentLost()
		fd.Close()
		return AdapterOutput{}, fail(Code("deadline_exceeded"), "private_deadline")
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			session.ParentLost()
			fd.Close()
		case <-done:
		}
	}()
	defer close(done)
	defer fd.Close()
	initial, e := session.InvokeFrame()
	if e != nil {
		return AdapterOutput{}, e
	}
	capBytes := session.invocation.request.Operation.WireCaps.AdapterFrameBytes
	if e = WriteFrame(fd, initial, capBytes); e != nil {
		session.ParentLost()
		return AdapterOutput{}, fail(ReconciliationRequired, "adapter_write")
	}
	for {
		raw, e := ReadFrame(fd, capBytes)
		if e != nil {
			certainty := session.ParentLost()
			if certainty == "uncertain" {
				return AdapterOutput{}, fail(ReconciliationRequired, "adapter_lost")
			}
			return AdapterOutput{}, fail(Canceled, "adapter_lost")
		}
		ack, result, e := session.Accept(ctx, raw)
		if result != nil {
			return *result, e
		}
		if e != nil {
			return AdapterOutput{}, e
		}
		if ack != nil {
			if e = WriteFrame(fd, ack, capBytes); e != nil {
				session.ParentLost()
				return AdapterOutput{}, fail(ReconciliationRequired, "ack_lost")
			}
		}
	}
}
