package sessionowner

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Retained red baseline: reviews/core-c1b2a-review-work/candidate/delta_review_test.go,
// SHA25640169d15b16652060a5405641c198ab14cf9a627b78a37d12adf1a7d31c8c9cb.
type a2IntentBackend struct {
	Backend
	mode          string
	denied, allow bool
	id            string
}

func (b *a2IntentBackend) Commit(ctx context.Context, g *Guard, e Expected, n *Snapshot, id string) (CommitReceipt, error) {
	current, loadErr := b.Backend.Load(ctx, g)
	if loadErr != nil {
		return CommitReceipt{}, loadErr
	}
	if !b.denied && a4TransactionStage(current, n, id) == "intent" {
		for _, j := range n.Journal {
			if j.Phase == "ACQUIRING" && j.Dispatches == 1 {
				b.denied = true
				b.id = id
				if b.mode == "ambiguous-committed" {
					if _, err := b.Backend.Commit(ctx, g, e, n, id); err != nil {
						return CommitReceipt{}, err
					}
				}
				certainty := "uncertain"
				if b.mode == "not-committed" {
					certainty = "not_committed"
				}
				return CommitReceipt{OperationID: id, Certainty: certainty}, nil
			}
		}
	}
	return b.Backend.Commit(ctx, g, e, n, id)
}
func (b *a2IntentBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.id && !b.allow && b.mode != "not-committed" {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func TestA2IR1DeniedIntentDoesNotConsumeAndCanResume(t *testing.T) {
	effects, proofs := 0, 0
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
				return AdapterOutput{}, e
			}
			effects++
			return AdapterOutput{Candidate: map[string]any{"token": "new-private"}, CandidateExpires: time.Now().Add(time.Hour)}, nil
		}
		if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
			return AdapterOutput{}, e
		}
		proofs++
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	})
	fault := &a2IntentBackend{Backend: b, mode: "not-committed"}
	o.backend = fault
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	id := b2ClaimID(t, first)
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	cl := s.Claims[id]
	if !fault.denied || effects != 0 || cl.AcquisitionAttempts != 0 || len(cl.EffectIDs) != 0 || cl.Phase != "WAIT_READINESS" {
		t.Fatalf("uncommitted proposal leaked: effects=%d count=%d phase=%s", effects, cl.AcquisitionAttempts, cl.Phase)
	}
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", id))))
	if got["kind"] != "recovered" || effects != 1 || proofs != 1 {
		t.Fatal("bounded no-effect continuation", got["kind"])
	}
	g, _ = b.Acquire(context.Background())
	s, _ = b.Load(context.Background(), g)
	g.Close()
	if s.Claims[id].AcquisitionAttempts != 1 || s.Claims[id].ProofAttempts != 1 {
		t.Fatal("committed permit lost")
	}
}
func TestA2IR1AmbiguousCommittedIntentIsNotRefunded(t *testing.T) {
	effects := 0
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
			return AdapterOutput{}, e
		}
		effects++
		return AdapterOutput{}, errors.New("unexpected")
	})
	fault := &a2IntentBackend{Backend: b, mode: "ambiguous-committed"}
	o.backend = fault
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	if got["certainty"] != "uncertain" || effects != 0 {
		t.Fatal("ambiguous permit")
	}
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	var cl *Claim
	for _, v := range s.Claims {
		cl = v
	}
	if cl == nil || cl.AcquisitionAttempts != 1 || len(cl.EffectIDs) != 1 {
		t.Fatal("consumed intent refunded")
	}
	marker := false
	for _, j := range s.Journal {
		_, prepared := j.Intents[fault.id]
		marker = marker || prepared
	}
	if !marker {
		t.Fatal("missing unresolved intent fence")
	}
	fresh, e := NewOwner(OwnerOptions{Registry: o.registry, Backend: fault, Authorizer: fixtureAuth{}, Adapters: o.adapters})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(o, fresh)
	ch, _ := fresh.AuthenticateChannel(context.Background(), "alice")
	status := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, fixtureRequest("inspect"))))
	if status["state"] != "uncertain" {
		t.Fatal("restart lost intent fence")
	}
	fault.allow = true
	resumed := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID))))
	if resumed["kind"] != "failed" || effects != 0 {
		t.Fatal("ambiguous intent replayed acquisition")
	}
	g, _ = b.Acquire(context.Background())
	s, _ = b.Load(context.Background(), g)
	g.Close()
	if s.Claims[cl.ID].AcquisitionAttempts != 1 || s.Claims[cl.ID].Phase != "EFFECT_UNCERTAIN" {
		t.Fatal("issued witness not preserved")
	}
}
func TestA2IR2SharedCandidateFences(t *testing.T) {
	for _, mode := range []string{"active-rejected", "prior-rejected", "valid-new"} {
		t.Run(mode, func(t *testing.T) {
			captures, proofs := 0, 0
			o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if i.Step() == "acquire_candidate" {
					captures++
					if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
						return AdapterOutput{}, e
					}
					return AdapterOutput{}, errors.New("lost capture")
				}
				proofs++
				if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
					return AdapterOutput{}, e
				}
				return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
			})
			candidate := map[string]any{"token": "synthetic-private-token"}
			if mode == "prior-rejected" {
				prior := b2Seed(t, o, b)
				g, _ := b.Acquire(context.Background())
				s, _ := b.Load(context.Background(), g)
				e := s.Expected()
				s.Claims[prior.ID].Phase = "REJECTED"
				s.Claims[prior.ID].Cause = "rejected_generation"
				candidate = cloneMap(s.Claims[prior.ID].Candidate)
				if _, err := b.Commit(context.Background(), g, e, s, "reject-prior"); err != nil {
					t.Fatal(err)
				}
				g.Close()
			} else {
				g, _ := b.Acquire(context.Background())
				s, _ := b.Load(context.Background(), g)
				e := s.Expected()
				s.AuthBlocked = true
				if _, err := b.Commit(context.Background(), g, e, s, "reject-active"); err != nil {
					t.Fatal(err)
				}
				g.Close()
				if mode == "valid-new" {
					candidate = map[string]any{"token": "different-valid-candidate"}
				}
			}
			original := o.adapters["fixture"].(fixtureAdapter)
			o.adapters["fixture"] = b2Reconciler{original, func(ctx context.Context, i *Invocation, in RecoveryReconcileRequest) (RecoveryReconcileResult, error) {
				if e := i.BeforeEffect(ctx, "RECONCILING"); e != nil {
					return RecoveryReconcileResult{}, e
				}
				return RecoveryReconcileResult{Request: in, Status: "candidate_ready", EvidenceID: "exact-provenance", Candidate: cloneMap(candidate), CandidateExpires: time.Now().Add(time.Hour)}, nil
			}}
			op := cloneOperation(o.registry.builtins["recover_resume"])
			op.BudgetPlans[0].PhaseMaxMs["action"] = 1000
			o.registry.builtins["recover_resume"] = op
			o.registry.operations[operationKey(op.ID, op.Version, op.RequestKind)] = op
			first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
			failure, _ := object(first["failure"])
			initialClaim, _ := object(first["claim"])
			if first["kind"] != "failed" || first["certainty"] != "uncertain" || text(failure, "category") != "reconciliation_required" || text(initialClaim, "phase") != "EFFECT_UNCERTAIN" || captures != 1 || proofs != 0 {
				t.Fatalf("IR2 initial state not established: kind=%v category=%v certainty=%v claim=%v captures=%d proofs=%d", first["kind"], failure["category"], first["certainty"], initialClaim != nil, captures, proofs)
			}
			id := b2ClaimID(t, first)
			got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", id))))
			g, _ := b.Acquire(context.Background())
			s, _ := b.Load(context.Background(), g)
			g.Close()
			if !candidateFenceOutcome(mode, got, captures, proofs, s, s.Claims[id]) {
				t.Fatalf("candidate fence failed: mode=%s kind=%v captures=%d proofs=%d authBlocked=%v", mode, got["kind"], captures, proofs, s.AuthBlocked)
			}

		})
	}
}

func TestA2IR1AmbiguousAbsentIntentWaitsForDefinitiveReadback(t *testing.T) {
	effects, proofs := 0, 0
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
				return AdapterOutput{}, e
			}
			effects++
			return AdapterOutput{Candidate: map[string]any{"token": "after-definite-readback"}, CandidateExpires: time.Now().Add(time.Hour)}, nil
		}
		if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
			return AdapterOutput{}, e
		}
		proofs++
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	})
	fault := &a2IntentBackend{Backend: b, mode: "ambiguous-absent"}
	o.backend = fault
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	if first["certainty"] != "uncertain" || effects != 0 {
		t.Fatal("ambiguous absence treated as permission")
	}
	g, _ := b.Acquire(context.Background())
	state, _ := b.Load(context.Background(), g)
	g.Close()
	var id string
	for key, cl := range state.Claims {
		id = key
		if cl.AcquisitionAttempts != 0 {
			t.Fatal("uncommitted counter persisted")
		}
	}
	fresh, e := NewOwner(OwnerOptions{Registry: o.registry, Backend: fault, Authorizer: fixtureAuth{}, Adapters: o.adapters, Proof: o.proof})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(o, fresh)
	ch, _ := fresh.AuthenticateChannel(context.Background(), "alice")
	refused := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", id))))
	if refused["kind"] != "failed" || effects != 0 {
		t.Fatal("unknown readback permitted retry")
	}
	fault.allow = true
	got := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", id))))
	if got["kind"] != "recovered" || effects != 1 || proofs != 1 {
		t.Fatal("positive noncommit did not permit bounded continuation", got["kind"])
	}
}
