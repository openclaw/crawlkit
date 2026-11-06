//go:build darwin

package sessionowner

import (
	"context"
	"strings"
	"testing"
	"time"
)

type handoffTransitionFault struct {
	Backend
	target                        string
	stored, allow, denyLater, hit bool
	targetID, transferKey         string
	lateFailures                  int
	attempts                      map[string]int
	reconciled                    map[string]int
}

func (b *handoffTransitionFault) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	if b.hit && b.denyLater {
		b.lateFailures++
		return nil, fail(StorageFailure, "fixture_later_bookkeeping")
	}
	return b.Backend.Load(ctx, g)
}
func (b *handoffTransitionFault) Commit(ctx context.Context, g *Guard, expected Expected, next *Snapshot, id string) (CommitReceipt, error) {
	current, e := b.Backend.Load(ctx, g)
	if e != nil {
		return CommitReceipt{}, e
	}
	stage := handoffTransactionStage(current, next, id)
	b.attempts[id]++
	if !b.hit && stage == b.target {
		b.hit = true
		b.targetID = id
		for key, tr := range next.CandidateTransfers {
			if tr.Delivery != nil && tr.Delivery.ID == id || tr.Retirement != nil && tr.Retirement.ID == id {
				b.transferKey = key
			}
		}
		if b.stored {
			if _, e = b.Backend.Commit(ctx, g, expected, next, id); e != nil {
				return CommitReceipt{}, e
			}
		}
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	if b.hit && b.denyLater {
		b.lateFailures++
		return CommitReceipt{}, fail(StorageFailure, "fixture_later_bookkeeping")
	}
	return b.Backend.Commit(ctx, g, expected, next, id)
}
func (b *handoffTransitionFault) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	b.reconciled[id]++
	if id == b.targetID && !b.allow {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func reopenHandoffOwner(t *testing.T, o *Owner, b Backend) (*Owner, *Channel) {
	t.Helper()
	fresh, e := NewOwner(OwnerOptions{Registry: o.registry, Backend: b, Authorizer: fixtureAuth{}, Adapters: o.adapters, Proof: o.proof, PrivateCandidates: o.candidateBindings})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(o, fresh)
	channel, e := fresh.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	return fresh, channel
}
func loadHandoffSnapshot(t *testing.T, b Backend) *Snapshot {
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
	return s
}

func TestB2B1ADeliveryAndRetirementExactReadback(t *testing.T) {
	for _, storageKind := range []string{"memory", "protected-file"} {
		for _, target := range []string{"delivery-commit", "retirement-commit"} {
			for _, stored := range []bool{false, true} {
				name := target + "/not-committed"
				if stored {
					name = target + "/committed"
				}
				t.Run(storageKind+"/"+name, func(t *testing.T) {
					o, memory, ch := fixtureRecoveryOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) { return AdapterOutput{}, nil })
					var raw Backend = memory
					fileRoot := ""
					if storageKind == "protected-file" {
						file, dir := fileFixture(t)
						fileRoot = dir
						initial := loadHandoffSnapshot(t, memory)
						g, e := file.Acquire(context.Background())
						if e != nil {
							t.Fatal(e)
						}
						empty, e := file.Load(context.Background(), g)
						if e != nil {
							t.Fatal(e)
						}
						if _, e = file.Commit(context.Background(), g, empty.Expected(), initial, "handoff-enrollment"); e != nil {
							t.Fatal(e)
						}
						g.Close()
						raw = file
					}
					configureHandoffRegistry(t, o)
					repo := &handoffRepository{material: map[string]CandidateMaterial{}}
					o.candidateBindings = map[string]CandidateBinding{"fixture": {Repository: repo, Expiry: ProtectedProducerExpiry}}
					fault := &handoffTransitionFault{Backend: raw, target: target, stored: stored, denyLater: true, attempts: map[string]int{}, reconciled: map[string]int{}}
					o.backend = fault
					captures, proofs, proofCalls, returns := 0, 0, 0, 0
					expiry := o.now().Add(time.Hour)
					firstRef := ""
					o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
						if i.Step() == "acquire_candidate" {
							captures++
							if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
								return AdapterOutput{}, e
							}
							return AdapterOutput{Candidate: map[string]any{"token": strings.Repeat("T", 6000)}, CandidateExpires: expiry}, nil
						}
						proofCalls++
						session, e := NewAdapterSession(i, "transition-parent")
						if e != nil {
							return AdapterOutput{}, e
						}
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
						ref := text(wire, "candidateRef")
						if firstRef == "" {
							firstRef = ref
						} else if ref == firstRef {
							t.Fatal("old reference reused")
						}
						m, e := session.ConsumeStagedCandidate(ctx, ref, int(num(wire["bytes"])), text(wire, "privateDigest"))
						if e != nil {
							if len(m.Raw) != 0 {
								t.Fatal("ambiguous delivery exposed bytes")
							}
							ack, _, permission := session.Accept(context.Background(), privateProofCheckpoint(t, session))
							if permission == nil || len(ack) != 0 || i.claim.ProofAttempts != 0 {
								t.Fatal("ambiguous delivery permitted proof")
							}
							return AdapterOutput{}, e
						}
						returns++
						if len(m.Raw) != 6012 {
							t.Fatal("wrong handoff bytes")
						}
						if target == "retirement-commit" && proofCalls == 1 {
							if session.ParentLost() != "not_dispatched" {
								t.Fatal("effect permission before checkpoint")
							}
							return AdapterOutput{}, fail(Canceled, "fixture_parent_lost_before_proof")
						}
						ack, _, e := session.Accept(ctx, privateProofCheckpoint(t, session))
						if e != nil || len(ack) == 0 {
							return AdapterOutput{}, e
						}
						proofs++
						return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
					}}
					first := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("new", ""))))
					if first["kind"] == "recovered" || captures != 1 || proofs != 0 {
						t.Fatal("initial stage missing")
					}
					claim := a4Claim(t, raw)
					if target == "retirement-commit" {
						if returns != 1 || fault.hit {
							t.Fatal("retirement setup missing")
						}
						first = resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", claim.ID))))
					}
					if !fault.hit || fault.targetID == "" || fault.transferKey == "" || fault.lateFailures == 0 || proofs != 0 {
						t.Fatal("target/later failure not hit", fault.hit, fault.lateFailures)
					}
					original := loadHandoffSnapshot(t, raw).CandidateTransfers[fault.transferKey]
					if original == nil {
						t.Fatal("durable transition absent")
					}
					transition := original.Delivery
					if target == "retirement-commit" {
						transition = original.Retirement
					}
					if transition == nil || transition.ID != fault.targetID {
						t.Fatal("original transition ID lost")
					}
					if !original.ExpiresAt.Equal(expiry) || a4Claim(t, raw).AcquisitionAttempts != 1 || a4Claim(t, raw).ProofAttempts != 0 {
						t.Fatal("scope/count/expiry changed")
					}
					fault.denyLater = false
					if fileRoot != "" {
						reopened, e := NewFileBackend(FileOptions{Directory: fileRoot, BindingID: "fixture-binding"})
						if e != nil {
							t.Fatal(e)
						}
						raw = reopened
						fault.Backend = reopened
					}
					fresh, freshChannel := reopenHandoffOwner(t, o, fault)
					writesBefore, readsBefore := repo.writes, repo.reads
					blocked := resultMap(t)(fresh.Handle(context.Background(), freshChannel, rawJSON(t, b2Request("resume", claim.ID))))
					if blocked["kind"] != "failed" || repo.writes != writesBefore || repo.reads != readsBefore || proofs != 0 || fault.reconciled[fault.targetID] < 2 {
						t.Fatal("unknown transition did not block exact resume")
					}
					// An ordinary operation also cannot route around an unresolved grant.
					ordinary := resultMap(t)(fresh.Handle(context.Background(), freshChannel, rawJSON(t, fixtureRequest("perform"))))
					if ordinary["kind"] == "performed" || repo.writes != writesBefore {
						t.Fatal("ordinary operation bypassed unknown transition")
					}
					fault.allow = true
					if fileRoot != "" {
						reopened, e := NewFileBackend(FileOptions{Directory: fileRoot, BindingID: "fixture-binding"})
						if e != nil {
							t.Fatal(e)
						}
						raw = reopened
						fault.Backend = reopened
					}
					resumed, resumeChannel := reopenHandoffOwner(t, o, fault)
					got := resultMap(t)(resumed.Handle(context.Background(), resumeChannel, rawJSON(t, b2Request("resume", claim.ID))))
					if got["kind"] != "recovered" || captures != 1 || proofs != 1 || proofCalls != 2 {
						t.Fatal("positive exact readback failed", got["kind"], captures, proofs, proofCalls)
					}
					end := loadHandoffSnapshot(t, raw)
					old := end.CandidateTransfers[fault.transferKey]
					if old.State != "retired" || old.Retirement == nil || old.Retirement.State != "committed" {
						t.Fatal("old grant not retired")
					}
					if old.Delivery == nil || old.Delivery.ID != original.Delivery.ID || old.ExpiresAt != original.ExpiresAt || old.SemanticDigest != original.SemanticDigest {
						t.Fatal("retained candidate/delivery changed")
					}
					wantAttempts := 1
					if target == "retirement-commit" && !stored {
						wantAttempts = 2
					}
					if fault.attempts[fault.targetID] != wantAttempts {
						t.Fatal("blind original transition retry", fault.attempts[fault.targetID], wantAttempts)
					}
					finalClaim := end.Claims[claim.ID]
					if finalClaim.AcquisitionAttempts != 1 || finalClaim.ProofAttempts != 1 || !finalClaim.CandidateExpires.Equal(expiry) {
						t.Fatal("attempt refund or expiry growth")
					}
					wantReturns := 1
					if target == "retirement-commit" {
						wantReturns = 2
					}
					if returns != wantReturns || repo.reads != 2 {
						t.Fatal("secret redelivery count", returns, repo.reads)
					}
				})
			}
		}
	}
}

func TestB2B1APreparationMustBePositiveBeforeTransition(t *testing.T) {
	for _, target := range []string{"delivery-prepare", "retirement-prepare"} {
		for _, stored := range []bool{false, true} {
			name := "not-stored"
			if stored {
				name = "stored"
			}
			t.Run(target+"/"+name, func(t *testing.T) {
				o, memory, ch := fixtureRecoveryOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) { return AdapterOutput{}, nil })
				configureHandoffRegistry(t, o)
				repo := &handoffRepository{material: map[string]CandidateMaterial{}}
				o.candidateBindings = map[string]CandidateBinding{"fixture": {Repository: repo, Expiry: ProtectedProducerExpiry}}
				fault := &handoffTransitionFault{Backend: memory, target: target, stored: stored, denyLater: true, attempts: map[string]int{}, reconciled: map[string]int{}}
				o.backend = fault
				calls, proofs := 0, 0
				expiry := o.now().Add(time.Hour)
				o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
					if i.Step() == "acquire_candidate" {
						if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
							return AdapterOutput{}, e
						}
						return AdapterOutput{Candidate: map[string]any{"token": strings.Repeat("P", 6000)}, CandidateExpires: expiry}, nil
					}
					calls++
					session, e := NewAdapterSession(i, "prepare-parent")
					if e != nil {
						return AdapterOutput{}, e
					}
					frame, e := session.InvokeFrame()
					if e != nil {
						return AdapterOutput{}, e
					}
					v, _ := DecodeJSON(frame, HardFrameLimit)
					invoke, _ := object(v)
					wire, _ := object(invoke["candidate"])
					m, e := session.ConsumeStagedCandidate(ctx, text(wire, "candidateRef"), int(num(wire["bytes"])), text(wire, "privateDigest"))
					if e != nil {
						if len(m.Raw) != 0 {
							t.Fatal("nonpositive preparation exposed candidate")
						}
						return AdapterOutput{}, e
					}
					if target == "retirement-prepare" && calls == 1 {
						session.ParentLost()
						return AdapterOutput{}, fail(Canceled, "fixture_before_proof")
					}
					ack, _, e := session.Accept(ctx, privateProofCheckpoint(t, session))
					if e != nil || len(ack) == 0 {
						return AdapterOutput{}, e
					}
					proofs++
					return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
				}}
				got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("new", ""))))
				if target == "retirement-prepare" {
					if got["kind"] == "recovered" || fault.hit || proofs != 0 {
						t.Fatal("retirement preparation setup")
					}
					got = resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", a4Claim(t, memory).ID))))
				}
				if got["kind"] == "recovered" || !fault.hit || fault.lateFailures == 0 || proofs != 0 {
					t.Fatal("preparation failure not exercised")
				}
				state := loadHandoffSnapshot(t, memory)
				for _, tr := range state.CandidateTransfers {
					transition := tr.Delivery
					if target == "retirement-prepare" {
						transition = tr.Retirement
					}
					if transition != nil && fault.attempts[transition.ID] != 0 {
						t.Fatal("transition attempted without positive preparation", target)
					}
				}
				id := a4Claim(t, memory).ID
				fault.denyLater = false
				fault.allow = true
				fresh, freshCh := reopenHandoffOwner(t, o, fault)
				got = resultMap(t)(fresh.Handle(context.Background(), freshCh, rawJSON(t, b2Request("resume", id))))
				if got["kind"] != "recovered" || proofs != 1 || calls != 2 {
					t.Fatal("prepared restart failed", got["kind"])
				}
			})
		}
	}
}
