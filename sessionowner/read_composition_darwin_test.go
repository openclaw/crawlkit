//go:build darwin

package sessionowner

import (
	"context"
	"testing"
	"time"
)

type composedOutcomeFault struct {
	Backend
	target, id                    string
	stored, allow, denyLater, hit bool
	laterFailures, readbacks      int
}

func (b *composedOutcomeFault) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	if b.hit && b.denyLater {
		b.laterFailures++
		return nil, fail(StorageFailure, "later_composition_bookkeeping")
	}
	return b.Backend.Load(ctx, g)
}
func (b *composedOutcomeFault) Commit(ctx context.Context, g *Guard, expected Expected, s *Snapshot, id string) (CommitReceipt, error) {
	hit := false
	for _, j := range s.Journal {
		if j.Composition != nil && j.Composition.Stage == "done" && j.Composition.FinishID == id && b.target == "final" {
			hit = true
		}
		if j.CompositionKey != "" {
			root := s.Journal[j.CompositionKey].Composition
			if r, ok := j.Intents[id]; ok && r.State == "committed" {
				if b.target == "initial-intent" && j.RequestID == root.InitialID || b.target == "retry-intent" && j.RequestID == root.RetryID {
					hit = true
				}
			}
			if b.target == "adoption" && s.Generation != expected.Generation {
				hit = true
			}
		}
	}
	if hit && !b.hit {
		b.hit = true
		b.id = id
		if b.stored {
			if _, e := b.Backend.Commit(ctx, g, expected, s, id); e != nil {
				return CommitReceipt{}, e
			}
		}
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	if b.hit && b.denyLater {
		b.laterFailures++
		return CommitReceipt{}, fail(StorageFailure, "later_composition_bookkeeping")
	}
	return b.Backend.Commit(ctx, g, expected, s, id)
}
func (b *composedOutcomeFault) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.id {
		b.readbacks++
		if !b.allow {
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
		}
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func reopenComposition(t *testing.T, old *Owner, backend Backend) (*Owner, *Channel) {
	t.Helper()
	o, e := NewOwner(OwnerOptions{Registry: old.registry, Backend: backend, Authorizer: fixtureAuth{}, Adapters: old.adapters, Proof: old.proof, ReadRecoveryOperations: []OperationRef{{"fixture.read", 1}}, ReadRecoveryAuthorization: old.readRecoveryAuthorization})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(old, o)
	ch, _ := o.AuthenticateChannel(context.Background(), "alice")
	return o, ch
}
func TestB2B2UnknownOriginalTransactionsNeverReplay(t *testing.T) {
	for _, storage := range []string{"memory", "protected-file"} {
		for _, target := range []string{"initial-intent", "adoption", "retry-intent", "final"} {
			for _, stored := range []bool{false, true} {
				name := storage + "/" + target + "/not-committed"
				if stored {
					name = storage + "/" + target + "/committed"
				}
				t.Run(name, func(t *testing.T) {
					o, memory, ch, _, counts := compositionFixture(t, "recover")
					var base Backend = memory
					dir := ""
					if storage == "protected-file" {
						file, path := fileFixture(t)
						dir = path
						initial := loadHandoffSnapshot(t, memory)
						g, _ := file.Acquire(context.Background())
						empty, _ := file.Load(context.Background(), g)
						if _, e := file.Commit(context.Background(), g, empty.Expected(), initial, "composition-enrollment"); e != nil {
							t.Fatal(e)
						}
						g.Close()
						base = file
					}
					fault := &composedOutcomeFault{Backend: base, target: target, stored: stored, denyLater: target != "final"}
					o.backend = fault
					q := compositionRequest(o)
					first := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
					if first["kind"] != "failed" || !fault.hit || fault.id == "" {
						t.Fatal("target not hit", target, first["kind"])
					}
					if target != "final" && fault.laterFailures == 0 {
						t.Fatal("later failure not exercised")
					}
					before := *counts
					state := loadHandoffSnapshot(t, base)
					rootKey := "alice\x00" + text(q, "requestId")
					root := state.Journal[rootKey]
					used, reserved, e := compositionUsage(state, rootKey)
					if e != nil || used > 4 || reserved > 256 {
						t.Fatal("bad accounting")
					}
					if target == "retry-intent" && !root.Composition.RetryReserved {
						t.Fatal("retry allowance not durable before attempt")
					}
					fault.denyLater = false
					if dir != "" {
						reopened, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
						if e != nil {
							t.Fatal(e)
						}
						base = reopened
						fault.Backend = reopened
					}
					fresh, c2 := reopenComposition(t, o, fault)
					q["deadline"] = timestamp(o.now().Add(time.Hour))
					blocked := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
					if blocked["kind"] != "failed" || *counts != before || fault.readbacks < 2 {
						t.Fatal("unknown readback replayed or skipped")
					}
					fault.allow = true
					fresh, c2 = reopenComposition(t, o, fault)
					after := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
					if target == "final" && stored {
						if after["kind"] != "performed" {
							t.Fatal("committed final not read back")
						}
					} else if after["kind"] != "failed" {
						t.Fatal("pending composition revived without original parent")
					}
					if *counts != before {
						t.Fatal("restart created extra effects")
					}
					state = loadHandoffSnapshot(t, base)
					nowUsed, nowReserved, e := compositionUsage(state, rootKey)
					if e != nil || nowUsed != used || nowReserved != reserved {
						t.Fatal("restart refunded/reset usage")
					}
				})
			}
		}
	}
}
func TestB2B2CancellationAndOriginalDeadline(t *testing.T) {
	for _, mode := range []string{"cancel-wire", "caller-cancel", "channel-close", "after-adoption"} {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, clock, counts := compositionFixture(t, "recover")
			q := compositionRequest(o)
			if mode == "after-adoption" {
				hook := &composedAdoptionClock{Backend: b, clock: clock}
				o.backend = hook
				got := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
				if !hook.hit || got["kind"] != "failed" || counts.read != 1 || counts.acquire != 1 || counts.proof != 1 {
					t.Fatal("post-adoption boundary not reached or retried")
				}
				state := loadHandoffSnapshot(t, b)
				if state.Generation == "epoch-1" {
					t.Fatal("adoption boundary not actually committed")
				}
				return
			}

			entered := make(chan struct{})
			base := o.adapters["fixture"].(fixtureAdapter)
			o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				out, e := base.Execute(ctx, i)
				if i.Step() == "execute" {
					close(entered)
					<-ctx.Done()
					return AdapterOutput{}, ctx.Err()
				}
				return out, e
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan []byte, 1)
			go func() { raw, _ := o.HandleWithRecovery(ctx, ch, rawJSON(t, q)); done <- raw }()
			<-entered
			switch mode {
			case "caller-cancel":
				cancel()
			case "channel-close":
				ch.Close()
			case "cancel-wire":
				cancelReq := fixtureRequest("cancel")
				cancelReq["targetRequestId"] = q["requestId"]
				cancelReq["reason"] = "caller_request"
				ack := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, cancelReq)))
				if ack["certainty"] != "uncertain" || ack["disposition"] != "cancellation_requested" {
					t.Fatal("root cancel lost child effect", ack)
				}
			}
			select {
			case raw := <-done:
				got := resultMap(t)(raw, nil)
				if got["kind"] != "failed" || counts.read != 1 || counts.acquire != 0 {
					t.Fatal("canceled composition continued")
				}
			case <-time.After(time.Second):
				t.Fatal("composition cancellation did not settle")
			}
			fresh, c2 := reopenComposition(t, o, b)
			q["deadline"] = timestamp(clock.Now().Add(time.Hour))
			prior := *counts
			got := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
			if got["kind"] != "failed" || *counts != prior {
				t.Fatal("fresh context revived canceled parent")
			}
		})
	}
}

type composedAdoptionClock struct {
	Backend
	clock *a2Clock
	hit   bool
}

func (b *composedAdoptionClock) Commit(ctx context.Context, g *Guard, e Expected, n *Snapshot, id string) (CommitReceipt, error) {
	r, err := b.Backend.Commit(ctx, g, e, n, id)
	if err == nil && !b.hit && n.Generation != e.Generation {
		for _, j := range n.Journal {
			if j.Composition != nil {
				b.hit = true
				b.clock.Advance(j.Composition.Deadline)
				break
			}
		}
	}
	return r, err
}

func TestB2B2RetainedClaimAndCooldown(t *testing.T) {
	for _, mode := range []string{"retained", "cooldown"} {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, clock, counts := compositionFixture(t, "recover")
			if mode == "retained" {
				claim := b2Seed(t, o, b)
				g, _ := b.Acquire(context.Background())
				s, _ := b.Load(context.Background(), g)
				expected := s.Expected()
				s.Claims[claim.ID].Cause = "expired_session"
				if _, e := b.Commit(context.Background(), g, expected, s, "composition-retained"); e != nil {
					t.Fatal(e)
				}
				g.Close()
				o.uncertainReads[operationKey("fixture.read", 1, "perform")] = true
			} else {
				g, _ := b.Acquire(context.Background())
				s, _ := b.Load(context.Background(), g)
				expected := s.Expected()
				s.Fences["recovery"] = clock.Now().Add(time.Hour)
				b.Commit(context.Background(), g, expected, s, "composition-fence")
				g.Close()
			}
			q := compositionRequest(o)
			got := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
			if mode == "retained" {
				if got["kind"] != "performed" || counts.read != 2 || counts.acquire != 0 || counts.proof != 1 {
					t.Fatal("retained claim recaptured", got["kind"], *counts)
				}
				if a4Claim(t, b).AcquisitionAttempts != 1 {
					t.Fatal("retained attempt refunded")
				}
			} else if got["kind"] != "failed" || counts.read != 1 || counts.acquire != 0 || counts.proof != 0 || got["retryNotBefore"] == nil {
				t.Fatal("cooldown bypassed")
			}
		})
	}
}
