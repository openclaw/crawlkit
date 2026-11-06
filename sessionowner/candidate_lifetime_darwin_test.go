//go:build darwin

package sessionowner

import (
	"context"
	"strings"
	"testing"
	"time"
)

type handoffRepository struct {
	material      map[string]CandidateMaterial
	reads, writes int
	onRead        func(context.Context, CandidateScope) error
}

func (r *handoffRepository) Write(ctx context.Context, s CandidateScope, raw []byte, expires time.Time, max int) (CandidateReference, error) {
	if ctx.Err() != nil {
		return CandidateReference{}, ctx.Err()
	}
	if len(raw) > max {
		return CandidateReference{}, fail(PayloadTooLarge, "fixture_cap")
	}
	id := newID()
	r.writes++
	r.material[id] = CandidateMaterial{Raw: append([]byte(nil), raw...), Scope: s, ExpiresAt: expires}
	return CandidateReference{id, len(raw), Hash(raw)}, nil
}
func (r *handoffRepository) Read(ctx context.Context, s CandidateScope, ref string, max int) (CandidateMaterial, error) {
	r.reads++
	m, ok := r.material[ref]
	if !ok {
		return CandidateMaterial{}, fail(ProtocolMismatch, "fixture_missing")
	}
	if r.onRead != nil {
		if e := r.onRead(ctx, s); e != nil {
			return CandidateMaterial{}, e
		}
	}
	m.Raw = append([]byte(nil), m.Raw...)
	return m, nil
}

type handoffStageBackend struct {
	Backend
	before, after func(context.Context, string)
	counts        map[string]int
	afterLoad     func(context.Context, *Snapshot)
}

func handoffTransactionStage(current, next *Snapshot, id string) string {
	for key, tr := range next.CandidateTransfers {
		old := current.CandidateTransfers[key]
		if tr.Delivery != nil {
			if tr.Delivery.ID == id && tr.State == "delivered" {
				return "delivery-commit"
			}
			if tr.Delivery.State == "prepared" && (old == nil || old.Delivery == nil) {
				return "delivery-prepare"
			}
		}
		if tr.Retirement != nil {
			if tr.Retirement.ID == id && tr.State == "retired" {
				return "retirement-commit"
			}
			if tr.Retirement.State == "prepared" && (old == nil || old.Retirement == nil) {
				return "retirement-prepare"
			}
		}
	}
	return "other"
}
func (b *handoffStageBackend) Commit(ctx context.Context, g *Guard, expected Expected, next *Snapshot, id string) (CommitReceipt, error) {
	current, e := b.Backend.Load(ctx, g)
	if e != nil {
		return CommitReceipt{}, e
	}
	stage := handoffTransactionStage(current, next, id)
	b.counts[stage]++
	if b.before != nil {
		b.before(ctx, stage)
	}
	receipt, e := b.Backend.Commit(ctx, g, expected, next, id)
	if b.after != nil {
		b.after(ctx, stage)
	}
	return receipt, e
}
func (b *handoffStageBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	s, e := b.Backend.Load(ctx, g)
	if e == nil && b.afterLoad != nil {
		b.afterLoad(ctx, s)
	}
	return s, e
}
func privateProofCheckpoint(t *testing.T, s *AdapterSession) []byte {
	return rawJSON(t, map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": s.lease, "sequence": "0", "phase": "PROVING", "event": "before_effect", "effectCertainty": "not_dispatched", "evidenceId": "handoff-proof", "evidence": map[string]any{"stage": "synthetic-before-effect"}})
}
func configureHandoffRegistry(t *testing.T, o *Owner) {
	o.registry = transferRegistry(t, 8192, 4096)
	for _, kind := range []string{"recover_new", "recover_resume"} {
		op := cloneOperation(o.registry.builtins[kind])
		ms := int64(5)
		op.BudgetPlans[0].MaxRPCMs = &ms
		o.registry.builtins[kind] = op
		o.registry.operations[operationKey(op.ID, op.Version, op.RequestKind)] = op
	}
}

func TestB2B1AConsumptionLifetimeBoundaries(t *testing.T) {
	cases := []string{"positive", "duplicate", "concurrent", "lease-read", "caller-read", "invocation-read", "candidate-read", "caller-storage", "invocation-storage", "candidate-storage", "caller-already-canceled", "invocation-cancel", "channel-close", "parent-read", "prepare-expiry", "pre-delivery-expiry", "delivery-expiry", "post-delivery-expiry", "prepare-parent", "delivery-parent", "post-delivery-parent"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			o, memory, ch := fixtureRecoveryOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) { return AdapterOutput{}, nil })
			configureHandoffRegistry(t, o)
			clock := &a2Clock{now: o.now()}
			o.now = clock.Now
			o.deadlineHook = clock.Until
			repo := &handoffRepository{material: map[string]CandidateMaterial{}}
			o.candidateBindings = map[string]CandidateBinding{"fixture": {Repository: repo, Expiry: ProtectedProducerExpiry}}
			storage := &handoffStageBackend{Backend: memory, counts: map[string]int{}}
			o.backend = storage
			expires := clock.Now().Add(time.Hour)
			if name == "candidate-read" || name == "candidate-storage" {
				expires = clock.Now().Add(3 * time.Millisecond)
			}
			hit := false
			proofs, captures, returned := 0, 0, 0
			var privateSession *AdapterSession
			o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if i.Step() == "acquire_candidate" {
					captures++
					if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
						return AdapterOutput{}, e
					}
					return AdapterOutput{Candidate: map[string]any{"token": strings.Repeat("L", 6000)}, CandidateExpires: expires}, nil
				}
				var stopOriginal context.CancelFunc
				if name == "invocation-read" || name == "invocation-storage" {
					i.operationCtx, stopOriginal = o.contextUntil(i.operationCtx, clock.Now().Add(2*time.Millisecond))
					defer stopOriginal()
				} else if name == "invocation-cancel" {
					i.operationCtx, stopOriginal = context.WithCancel(i.operationCtx)
					defer stopOriginal()
				}
				session, e := NewAdapterSession(i, "bounded-handoff-parent")
				if e != nil {
					return AdapterOutput{}, e
				}
				privateSession = session
				frame, e := session.InvokeFrame()
				if e != nil {
					return AdapterOutput{}, e
				}
				v, e := DecodeJSON(frame, HardFrameLimit)
				if e != nil {
					return AdapterOutput{}, e
				}
				invoke, _ := object(v)
				wire, _ := object(invoke["candidate"])
				if text(wire, "mode") != "staged" || storage.counts["delivery-commit"] != 0 {
					t.Fatal("initial issued stage not reached")
				}
				caller := context.Background()
				if name == "caller-read" || name == "caller-storage" {
					var cancel context.CancelFunc
					caller, cancel = o.contextUntil(caller, clock.Now().Add(time.Millisecond))
					defer cancel()
				}
				if name == "caller-already-canceled" {
					var cancel context.CancelFunc
					caller, cancel = context.WithCancel(caller)
					cancel()
					hit = true
				}
				want := clock.Now().Add(5 * time.Millisecond)
				if name == "caller-read" || name == "caller-storage" {
					want = clock.Now().Add(time.Millisecond)
				}
				if name == "invocation-read" || name == "invocation-storage" {
					want = clock.Now().Add(2 * time.Millisecond)
				}
				if name == "candidate-read" || name == "candidate-storage" {
					want = expires
				}
				loseParent := func(bound context.Context) {
					hit = true
					done := make(chan string, 1)
					go func() { done <- session.ParentLost() }()
					select {
					case certainty := <-done:
						if certainty != "not_dispatched" {
							t.Error("proof permitted before delivery")
						}
					case <-time.After(250 * time.Millisecond):
						t.Error("ParentLost blocked on session mutex")
						return
					}
					select {
					case <-bound.Done():
					case <-time.After(250 * time.Millisecond):
						t.Error("parent loss did not cancel pending work")
					}
				}
				repo.onRead = func(bound context.Context, _ CandidateScope) error {
					d, ok := bound.Deadline()
					if !ok || !d.Equal(want) {
						t.Errorf("handoff bound %v want %v", d, want)
					}
					switch name {
					case "lease-read", "caller-read", "invocation-read", "candidate-read":
						hit = true
						clock.Advance(d)
					case "invocation-cancel":
						hit = true
						stopOriginal()
					case "channel-close":
						hit = true
						ch.Close()
					case "parent-read":
						loseParent(bound)
					}
					return nil
				}
				storage.before = func(bound context.Context, stage string) {
					if hit {
						return
					}
					if name == "prepare-expiry" && stage == "delivery-prepare" || name == "delivery-expiry" && stage == "delivery-commit" {
						hit = true
						clock.Advance(want)
					}
					if name == "prepare-parent" && stage == "delivery-prepare" || name == "delivery-parent" && stage == "delivery-commit" {
						loseParent(bound)
					}
				}
				storage.after = func(bound context.Context, stage string) {
					if hit {
						return
					}
					if (name == "post-delivery-expiry" || name == "caller-storage" || name == "invocation-storage" || name == "candidate-storage") && stage == "delivery-commit" {
						hit = true
						clock.Advance(want)
					}
					if name == "post-delivery-parent" && stage == "delivery-commit" {
						loseParent(bound)
					}
				}
				storage.afterLoad = func(bound context.Context, s *Snapshot) {
					if name != "pre-delivery-expiry" || hit {
						return
					}
					for _, tr := range s.CandidateTransfers {
						if tr.Delivery != nil && tr.Delivery.State == "prepared" {
							hit = true
							clock.Advance(want)
							return
						}
					}
				}
				consume := func() (CandidateMaterial, error) {
					return session.ConsumeStagedCandidate(caller, text(wire, "candidateRef"), int(num(wire["bytes"])), text(wire, "privateDigest"))
				}
				var material CandidateMaterial
				if name == "concurrent" {
					type response struct {
						m CandidateMaterial
						e error
					}
					responses := make(chan response, 2)
					for n := 0; n < 2; n++ {
						go func() { m, e := consume(); responses <- response{m, e} }()
					}
					success := 0
					for n := 0; n < 2; n++ {
						a := <-responses
						if a.e == nil {
							success++
							material = a.m
						} else if len(a.m.Raw) != 0 {
							t.Fatal("failed concurrent consume leaked bytes")
						}
					}
					if success != 1 {
						t.Fatal("concurrent use count", success)
					}
				} else {
					material, e = consume()
				}
				returned = len(material.Raw)
				positive := name == "positive" || name == "duplicate" || name == "concurrent"
				if positive {
					if e != nil || returned != 6012 {
						t.Fatal("positive bounded handoff", e, returned)
					}
					if name == "duplicate" {
						again, err := consume()
						if err == nil || len(again.Raw) != 0 {
							t.Fatal("duplicate consumed")
						}
					}
					ack, _, err := session.Accept(ctx, privateProofCheckpoint(t, session))
					if err != nil || len(ack) == 0 {
						t.Fatal("positive proof checkpoint", err)
					}
					proofs++
					return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
				}
				if !hit || e == nil || returned != 0 || material.Scope != (CandidateScope{}) || !material.ExpiresAt.IsZero() {
					t.Fatal("boundary failed to withhold material", hit, e, returned)
				}
				readsBeforeRetry := repo.reads
				again, retryErr := session.ConsumeStagedCandidate(context.Background(), text(wire, "candidateRef"), int(num(wire["bytes"])), text(wire, "privateDigest"))
				if retryErr == nil || len(again.Raw) != 0 || repo.reads != readsBeforeRetry {
					t.Fatal("fresh caller context revived refused handoff")
				}
				ack, out, permissionErr := session.Accept(context.Background(), privateProofCheckpoint(t, session))
				if permissionErr == nil || len(ack) != 0 || out != nil || i.claim.ProofAttempts != 0 {
					t.Fatal("proof permitted after refused handoff")
				}
				return AdapterOutput{}, e
			}}
			got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("new", ""))))
			positive := name == "positive" || name == "duplicate" || name == "concurrent"
			if positive {
				if got["kind"] != "recovered" || proofs != 1 || storage.counts["delivery-prepare"] != 1 || storage.counts["delivery-commit"] != 1 {
					t.Fatal("positive stages", got["kind"], storage.counts)
				}
			} else if got["kind"] == "recovered" || proofs != 0 || !hit || returned != 0 {
				t.Fatal("late handoff adopted")
			}
			expectedReads := 1
			if name == "caller-already-canceled" {
				expectedReads = 0
			}
			if captures != 1 || repo.reads != expectedReads || privateSession == nil {
				t.Fatal("fixture stage missing", captures, repo.reads)
			}
		})
	}
}
