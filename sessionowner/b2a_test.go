package sessionowner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func b2Request(mode, id string) map[string]any {
	q := fixtureRequest("recover")
	q["mode"] = mode
	q["expectedGeneration"] = "epoch-1"
	q["recoveryAuthorizationRef"] = "policy-1"
	if mode == "new" {
		q["cause"] = "authorized_replacement"
		q["rejectedRequestId"] = nil
	} else {
		q["claimId"] = id
	}
	return q
}
func b2ClaimID(t *testing.T, m map[string]any) string {
	t.Helper()
	c, ok := object(m["claim"])
	if !ok {
		t.Fatalf("missing claim in %v", m["kind"])
	}
	return text(c, "claimId")
}
func b2Seed(t *testing.T, o *Owner, b Backend) *Claim {
	t.Helper()
	g, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	s, e := b.Load(context.Background(), g)
	if e != nil {
		t.Fatal(e)
	}
	expected := s.Expected()
	c := &Claim{ID: "retained-claim", BindingID: s.BindingID, Caller: "alice", Epoch: s.Generation, Cause: "authorized_replacement", OriginRequestID: "origin", OriginJournalKey: "alice\x00origin", Phase: "CANDIDATE_READY", AcquisitionAttempts: 1, Candidate: map[string]any{"token": "retained-private"}, CandidateExpires: time.Now().Add(time.Hour)}
	c.CandidateDigest = payloadDigest(c.Candidate)
	s.Claims[c.ID] = c
	s.Journal[c.OriginJournalKey] = &JournalEntry{Caller: c.Caller, RequestID: "origin", Generation: c.Epoch, RequestKind: "recover", ClaimID: c.ID, Semantics: "recovery", Certainty: "completed", ReceiptID: "origin-receipt"}
	if _, e = b.Commit(context.Background(), g, expected, s, "seed-claim"); e != nil {
		t.Fatal(e)
	}
	return c
}
func TestB2ARecoveringExclusionAndExactResume(t *testing.T) {
	var captures, proofs int
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			captures++
			t.Fatal("retained candidate reacquired")
		}
		if i.Step() == "prove_candidate" {
			proofs++
			if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
		}
		t.Fatal("ordinary mutation reached adapter")
		return AdapterOutput{}, nil
	})
	claim := b2Seed(t, o, b)
	status := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("inspect"))))
	if status["state"] != "recovering" {
		t.Fatal("state")
	}
	write := fixtureRequest("perform")
	write["operationId"] = "fixture.write"
	write["confirmationRef"] = "approval-1"
	for _, q := range []map[string]any{write, b2Request("new", "")} {
		got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
		if got["kind"] != "failed" {
			t.Fatal("recovering bypass")
		}
	}
	bob, _ := o.AuthenticateChannel(context.Background(), "bob")
	wrong := resultMap(t)(o.Handle(context.Background(), bob, rawJSON(t, b2Request("resume", claim.ID))))
	if wrong["kind"] != "failed" {
		t.Fatal("wrong caller")
	}
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", claim.ID))))
	if got["kind"] != "recovered" || captures != 0 || proofs != 1 {
		t.Fatalf("resume kind=%v proof=%d", got["kind"], proofs)
	}
	again := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", claim.ID))))
	if again["kind"] != "recovered" || again["receiptRef"] != got["receiptRef"] || proofs != 1 {
		t.Fatal("retained commit readback")
	}
}
func TestB2ARealDiagnosisNotLatchOrReceiptAlone(t *testing.T) {
	var captures int
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "execute" {
			if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{}, fail("rejected_generation", "qualified synthetic rejection")
		}
		if i.Step() == "acquire_candidate" {
			captures++
			if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{Candidate: map[string]any{"token": "new-private"}, CandidateExpires: time.Now().Add(time.Hour)}, nil
		}
		if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
			return AdapterOutput{}, e
		}
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	})
	read := fixtureRequest("perform")
	rejection := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, read)))
	if rejection["certainty"] != "completed" {
		t.Fatal("diagnosed read outcome")
	}
	for _, pair := range [][2]string{{"unknown", "rejected_generation"}, {text(read, "requestId"), "expired_session"}} {
		q := b2Request("new", "")
		q["cause"] = pair[1]
		q["rejectedRequestId"] = pair[0]
		got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
		if got["kind"] != "failed" || captures != 0 {
			t.Fatal("forged diagnosis accepted")
		}
	}
	q := b2Request("new", "")
	q["cause"] = "rejected_generation"
	q["rejectedRequestId"] = read["requestId"]
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
	if got["kind"] != "recovered" || captures != 1 {
		t.Fatalf("real rejection: %v", got["kind"])
	}
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	for _, cl := range s.Claims {
		if cl.DiagnosisID == "" {
			t.Fatal("diagnosis not bound")
		}
	}
}

type b2Reconciler struct {
	fixtureAdapter
	reconcile func(context.Context, *Invocation, RecoveryReconcileRequest) (RecoveryReconcileResult, error)
}

func (a b2Reconciler) ReconcileRecovery(ctx context.Context, i *Invocation, r RecoveryReconcileRequest) (RecoveryReconcileResult, error) {
	return a.reconcile(ctx, i, r)
}
func TestB2AUncertainEffectReconcilesWithoutCaptureReplay(t *testing.T) {
	var captures, proofs, lookups int
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		switch i.Step() {
		case "acquire_candidate":
			captures++
			if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{}, errors.New("lost capture result")
		case "prove_candidate":
			proofs++
			if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
		}
		return AdapterOutput{}, errors.New("unexpected")
	})
	original := o.adapters["fixture"].(fixtureAdapter)
	o.adapters["fixture"] = b2Reconciler{original, func(ctx context.Context, i *Invocation, r RecoveryReconcileRequest) (RecoveryReconcileResult, error) {
		lookups++
		if e := i.BeforeEffect(ctx, "RECONCILING"); e != nil {
			return RecoveryReconcileResult{}, e
		}
		return RecoveryReconcileResult{Request: r, Status: "candidate_ready", EvidenceID: "retained-effect-proof", Candidate: map[string]any{"token": "already-captured"}, CandidateExpires: time.Now().Add(time.Hour)}, nil
	}}
	op := cloneOperation(o.registry.builtins["recover_resume"])
	op.BudgetPlans[0].PhaseMaxMs["action"] = 1000
	o.registry.builtins["recover_resume"] = op
	o.registry.operations[operationKey(op.ID, op.Version, op.RequestKind)] = op
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	id := b2ClaimID(t, first)
	if first["certainty"] != "uncertain" {
		t.Fatal("lost effect")
	}
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", id))))
	if got["kind"] != "recovered" || captures != 1 || proofs != 1 || lookups != 1 {
		t.Fatalf("kind=%v captures=%d proof=%d lookup=%d", got["kind"], captures, proofs, lookups)
	}
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	if s.Claims[id].AcquisitionAttempts != 1 {
		t.Fatal("attempt refunded")
	}
}

type b2CommitFault struct {
	Backend
	mode, id       string
	tripped, allow bool
	adoptions      int
}

func (b *b2CommitFault) Commit(ctx context.Context, g *Guard, e Expected, n *Snapshot, id string) (CommitReceipt, error) {
	if n.Generation != e.Generation {
		b.adoptions++
		if !b.tripped {
			b.tripped = true
			b.id = id
			if b.mode == "committed" {
				if _, er := b.Backend.Commit(ctx, g, e, n, id); er != nil {
					return CommitReceipt{}, er
				}
			}
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
		}
	}
	return b.Backend.Commit(ctx, g, e, n, id)
}
func (b *b2CommitFault) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.id && !b.allow {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func TestB2AExactCommitReconciliation(t *testing.T) {
	for _, mode := range []string{"committed", "not_committed"} {
		t.Run(mode, func(t *testing.T) {
			var captures, proofs int
			o, base, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if i.Step() == "acquire_candidate" {
					captures++
					if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
						return AdapterOutput{}, e
					}
					return AdapterOutput{Candidate: map[string]any{"token": "commit-candidate"}, CandidateExpires: time.Now().Add(time.Hour)}, nil
				}
				proofs++
				if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
					return AdapterOutput{}, e
				}
				return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
			})
			fault := &b2CommitFault{Backend: base, mode: mode}
			o.backend = fault
			first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
			id := b2ClaimID(t, first)
			if first["certainty"] != "uncertain" {
				t.Fatal("commit fault not surfaced")
			}
			fault.allow = true
			got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", id))))
			if got["kind"] != "recovered" || captures != 1 || proofs != 1 {
				t.Fatalf("mode=%s result=%v capture=%d proof=%d", mode, got["kind"], captures, proofs)
			}
			want := 1
			if mode == "not_committed" {
				want = 2
			}
			if fault.adoptions != want {
				t.Fatal("blind repeated commit")
			}
		})
	}
}
func TestB2ABeforePermitCancellationAndExpiredCandidate(t *testing.T) {
	var captures, proofs int
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			captures++
			if captures == 1 {
				return AdapterOutput{}, context.Canceled
			}
			if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{Candidate: map[string]any{"token": "after-pause"}, CandidateExpires: time.Now().Add(time.Hour)}, nil
		}
		proofs++
		if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
			return AdapterOutput{}, e
		}
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	})
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	id := b2ClaimID(t, first)
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	if s.Claims[id].AcquisitionAttempts != 0 || s.Claims[id].Phase != "WAIT_READINESS" {
		t.Fatal("pre-permit attempt consumed")
	}
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", id))))
	if got["kind"] != "recovered" || captures != 2 || proofs != 1 {
		t.Fatal("finite same claim resume")
	}
	o, b, c = fixtureRecoveryOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) {
		t.Fatal("expired candidate invoked adapter")
		return AdapterOutput{}, nil
	})
	cl := b2Seed(t, o, b)
	g, _ = b.Acquire(context.Background())
	s, _ = b.Load(context.Background(), g)
	e := s.Expected()
	s.Claims[cl.ID].CandidateExpires = time.Now().Add(-time.Second)
	b.Commit(context.Background(), g, e, s, "expire")
	g.Close()
	got = resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", cl.ID))))
	if got["kind"] != "failed" {
		t.Fatal("expired candidate")
	}
}
