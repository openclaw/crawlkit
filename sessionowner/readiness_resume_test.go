package sessionowner

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"
)

type c5ReadinessTrace struct {
	readiness, acquisition, proof, ordinary int
	uncertain                               bool
	ackIDs                                  []string
	ackReceipts                             []CommitReceipt
	steps                                   []string
}

func c5ReadyOwner(t *testing.T, backend Backend, uncertain bool) (*Owner, Backend, *Channel, *a2Clock, *c5ReadinessTrace) {
	t.Helper()
	trace := &c5ReadinessTrace{uncertain: uncertain}
	var clock *a2Clock
	template, memory, _ := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		trace.steps = append(trace.steps, i.Step())
		switch i.Step() {
		case "readiness":
			trace.readiness++
			if i.plan.Readiness == "none" || i.owner.warm() {
				t.Fatal("cold readiness branch not reached")
			}
			if trace.uncertain {
				if e := i.BeforeEffect(ctx, "WAIT_READINESS"); e != nil {
					return AdapterOutput{}, e
				}
				return AdapterOutput{}, fail(Code("browser_unavailable"), "c5-uncertain-readiness")
			}
			session, e := NewAdapterSession(i, "c5-readiness-parent")
			if e != nil {
				return AdapterOutput{}, e
			}
			defer session.cancelParent()
			if _, e = session.InvokeFrame(); e != nil {
				return AdapterOutput{}, e
			}
			evidenceID := fmt.Sprintf("c5-ready-noeffect-%d", trace.readiness)
			frame := map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": cloneMap(session.lease), "sequence": "0", "phase": "WAIT_READINESS", "event": "paused_no_effect", "effectCertainty": "not_dispatched", "evidenceId": evidenceID, "evidence": map[string]any{"stage": "synthetic-no-effect"}}
			ack, out, e := session.Accept(ctx, rawJSON(t, frame))
			if e != nil || len(ack) == 0 || out != nil {
				t.Fatal("positive no-effect checkpoint ACK absent", e)
			}
			value, e := DecodeJSON(ack, HardFrameLimit)
			if e != nil {
				t.Fatal(e)
			}
			a, _ := object(value)
			if a["authorizedToProceed"] != true || text(a, "checkpointSequence") != "0" || text(a, "durableStateRevision") != strconv.FormatUint(i.snapshot.StateRevision, 10) || session.permitted.Load() || i.dispatched() || len(i.claim.EffectIDs) != 0 || text(session.noEffect, "stage") != "synthetic-no-effect" {
				t.Fatal("no-effect ACK not exact/positive")
			}
			found := false
			for _, receipt := range i.snapshot.Commits {
				if receipt.StateRevision == i.snapshot.StateRevision {
					native, err := i.owner.backend.Reconcile(ctx, i.guard, receipt.OperationID)
					if err != nil || !receiptMatches(native, receipt) {
						t.Fatal("checkpoint native receipt not durable", err)
					}
					trace.ackReceipts = append(trace.ackReceipts, native)
					found = true
					break
				}
			}
			if !found {
				t.Fatal("checkpoint transaction missing")
			}
			trace.ackIDs = append(trace.ackIDs, evidenceID)
			if trace.readiness == 1 {
				return AdapterOutput{NoEffectEvidence: cloneMap(session.noEffect)}, fail(Code("browser_unavailable"), "c5-transient")
			}
			return AdapterOutput{NoEffectEvidence: cloneMap(session.noEffect)}, nil
		case "acquire_candidate":
			if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
				return AdapterOutput{}, e
			}
			trace.acquisition++
			return AdapterOutput{Candidate: map[string]any{"token": "c5-private-candidate"}, CandidateExpires: clock.Now().Add(time.Hour)}, nil
		case "prove_candidate":
			if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
				return AdapterOutput{}, e
			}
			trace.proof++
			return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
		default:
			trace.ordinary++
			return AdapterOutput{}, fail(ProtocolMismatch, "unexpected-c5-step")
		}
	})
	clock = &a2Clock{now: template.now()}
	template.now, template.deadlineHook = clock.Now, clock.Until
	for key, op := range template.registry.operations {
		if op.RequestKind != "recover_new" && op.RequestKind != "recover_resume" {
			continue
		}
		op = cloneOperation(op)
		op.MinimumRetryDelayMs = 1000
		for n := range op.BudgetPlans {
			op.BudgetPlans[n].Readiness = "cold_allowed"
			op.BudgetPlans[n].PhaseMaxMs["startup"] = 100
		}
		template.registry.operations[key] = op
		template.registry.builtins[op.RequestKind] = op
	}
	if backend == nil {
		backend = memory
	} else {
		initial := loadHandoffSnapshot(t, memory)
		g, e := backend.Acquire(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		empty, e := backend.Load(context.Background(), g)
		if e != nil {
			g.Close()
			t.Fatal(e)
		}
		_, e = backend.Commit(context.Background(), g, empty.Expected(), initial, "c5-test-enrollment")
		g.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	fresh, e := NewOwner(OwnerOptions{Registry: template.registry, Backend: backend, Authorizer: fixtureAuth{}, Adapters: template.adapters, Proof: template.proof, WarmReady: func() bool { return false }})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(template, fresh)
	ch, e := fresh.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(ch.Close)
	return fresh, backend, ch, clock, trace
}
func c5ActualPause(t *testing.T, o *Owner, b Backend, ch *Channel, trace *c5ReadinessTrace) (*Snapshot, string) {
	t.Helper()
	before := loadHandoffSnapshot(t, b)
	got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("new", ""))))
	id := b2ClaimID(t, got)
	s := loadHandoffSnapshot(t, b)
	c := s.Claims[id]
	if got["kind"] != "failed" || text(mapValue(got["failure"]), "category") != "browser_unavailable" || got["certainty"] != "not_dispatched" || trace.readiness != 1 || trace.acquisition != 0 || trace.proof != 0 || trace.ordinary != 0 || len(trace.ackReceipts) != 1 || len(trace.ackIDs) != 1 {
		t.Fatal("actual transient readiness stage/outcome not reached", got, trace.steps)
	}
	if c == nil || c.Phase != "WAIT_READINESS" || !c.NoEffectProved || c.AcquisitionAttempts != 0 || c.ProofAttempts != 0 || c.ReadinessResumptions != 0 || len(c.EffectIDs) != 0 || c.ActiveStep != "" || !c.RetryNotBefore.Equal(s.Fences["recovery"]) || !c.RetryNotBefore.After(o.now()) {
		t.Fatal("readiness pause/fence/counters")
	}
	j := s.Journal[c.OriginJournalKey]
	if j == nil || j.ClaimID != id || j.RequestID != c.OriginRequestID || j.Dispatches != 0 || len(j.Intents) != 0 || len(j.EffectIDs) != 0 || !validID(j.ResolutionID) || got["receiptRef"] != j.ReceiptID {
		t.Fatal("exact original readiness journal/evidence missing")
	}
	if s.Generation != before.Generation || s.CredentialRevision != before.CredentialRevision || payloadDigest(s.Credentials) != payloadDigest(before.Credentials) {
		t.Fatal("transient readiness changed active credentials")
	}
	for key, receipt := range before.Commits {
		if !receiptMatches(s.Commits[key], receipt) {
			t.Fatal("original commit lost")
		}
	}
	t.Logf("C5_STAGE readiness_pause claim=%s origin=%s resolutionEvidence=%s checkpointEvidence=%s checkpointTransaction=%s attempts=0/0 resumptions=0", id, c.OriginRequestID, j.ResolutionID, trace.ackIDs[0], trace.ackReceipts[0].OperationID)
	return s, id
}
func c5Continue(t *testing.T, o *Owner, b Backend, ch *Channel, clock *a2Clock, trace *c5ReadinessTrace, paused *Snapshot, id string, beforeFence bool) {
	t.Helper()
	original := paused.Claims[id]
	originalJournal := paused.Journal[original.OriginJournalKey]
	fence := paused.Fences["recovery"]
	if beforeFence {
		oldCounts := [3]int{trace.readiness, trace.acquisition, trace.proof}
		got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", id))))
		after := loadHandoffSnapshot(t, b)
		if got["kind"] != "failed" || text(mapValue(got["failure"]), "category") != "throttled" || got["certainty"] != "not_dispatched" || [3]int{trace.readiness, trace.acquisition, trace.proof} != oldCounts || payloadDigest(after.Claims[id]) != payloadDigest(original) || payloadDigest(after.Journal[original.OriginJournalKey]) != payloadDigest(originalJournal) || !after.Fences["recovery"].Equal(fence) {
			t.Fatal("before-fence control changed readiness/history")
		}
		t.Log("C5_CONTROL before_fence: throttled,no callbacks/permits,original claim/origin evidence/fence unchanged")
	}
	clock.Advance(fence)
	got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", id))))
	after := loadHandoffSnapshot(t, b)
	claim := after.Claims[id]
	if got["kind"] != "recovered" || b2ClaimID(t, got) != id || trace.readiness != 2 || trace.acquisition != 1 || trace.proof != 1 || trace.ordinary != 0 || len(trace.ackReceipts) != 2 || claim.Phase != "ACCEPTED" || claim.ReadinessResumptions != 1 || claim.AcquisitionAttempts != 1 || claim.ProofAttempts != 1 || claim.OriginJournalKey != original.OriginJournalKey || claim.OriginRequestID != original.OriginRequestID || !after.Fences["recovery"].Equal(fence) {
		t.Fatal("same claim continuation/counters/fence", got["kind"], trace.steps)
	}
	old := after.Journal[original.OriginJournalKey]
	if old.ReceiptID != originalJournal.ReceiptID || old.RequestID != originalJournal.RequestID || old.Digest != originalJournal.Digest || old.Dispatches != 0 || !bytes.Equal(old.Response, originalJournal.Response) {
		t.Fatal("resume rewrote original readiness response/identity")
	}
	for key, receipt := range paused.Commits {
		if !receiptMatches(after.Commits[key], receipt) {
			t.Fatal("paused native receipt overwritten")
		}
	}
	for _, receipt := range trace.ackReceipts {
		if !receiptMatches(after.Commits[receipt.OperationID], receipt) {
			t.Fatal("positive no-effect checkpoint receipt lost")
		}
	}
	t.Logf("C5_STAGE resumed sameClaim=%s readiness=2 acquisition=1 proof=1 resumptions=1 originalReceipt=%s", id, originalJournal.ReceiptID)
}
func TestC5R1ColdReadinessMemory(t *testing.T) {
	o, b, ch, clock, trace := c5ReadyOwner(t, nil, false)
	paused, id := c5ActualPause(t, o, b, ch, trace)
	c5Continue(t, o, b, ch, clock, trace, paused, id, true)
}
