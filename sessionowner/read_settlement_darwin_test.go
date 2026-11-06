//go:build darwin

package sessionowner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func settlementFixture(t *testing.T, stage string, stored bool) (*Owner, Backend, *Channel, map[string]any, *compositionCounts) {
	return settlementStorageFixture(t, stage, stored, false)
}
func settlementStorageFixture(t *testing.T, stage string, stored, fileStorage bool) (*Owner, Backend, *Channel, map[string]any, *compositionCounts) {
	o, b, ch, _, counts := compositionFixture(t, "recover")
	var backend Backend = b
	if fileStorage {
		file, _ := fileFixture(t)
		initial := loadHandoffSnapshot(t, b)
		g, _ := file.Acquire(context.Background())
		empty, _ := file.Load(context.Background(), g)
		if _, e := file.Commit(context.Background(), g, empty.Expected(), initial, "settlement-enrollment"); e != nil {
			t.Fatal(e)
		}
		g.Close()
		backend = file
	}
	fault := &composedOutcomeFault{Backend: backend, target: stage, stored: stored, denyLater: true}
	o.backend = fault
	q := compositionRequest(o)
	got := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
	if !fault.hit || got["kind"] != "failed" {
		t.Fatal("interrupted fixture not reached")
	}
	fault.allow = true
	fault.denyLater = false
	o.backend = backend
	o.settlementAuthorization = func(ctx context.Context, d ReadSettlementDescriptor) error { return nil }
	return o, backend, ch, q, counts
}
func freshSettlement(t *testing.T, o *Owner, b Backend) (*Owner, *Channel) {
	fresh, ch := reopenComposition(t, o, b)
	fresh.settlementAuthorization = o.settlementAuthorization
	return fresh, ch
}
func assertSettlementPreserved(t *testing.T, before, after *Snapshot, key string) {
	t.Helper()
	a, _ := before.Clone()
	z, _ := after.Clone()
	a.StateRevision = 0
	z.StateRevision = 0
	a.Commits = map[string]CommitReceipt{}
	z.Commits = map[string]CommitReceipt{}
	a.Journal[key].Composition.Settlement = nil
	z.Journal[key].Composition.Settlement = nil
	if !sameSnapshot(a, z) {
		t.Fatal("settlement changed original history/counters/authority")
	}
}
func TestB3SafeSettlementAndHistorical(t *testing.T) {
	for _, scenario := range []string{"no-effect", "adopted-no-retry", "issued-missing-result", "historical"} {
		t.Run(scenario, func(t *testing.T) {
			target, stored := "initial-intent", false
			if scenario == "adopted-no-retry" {
				target = "retry-intent"
			}
			if scenario == "issued-missing-result" {
				stored = true
			}
			if scenario == "historical" {
				target = "final"
				stored = true
			}
			o, b, ch, q, counts := settlementFixture(t, target, stored)
			key := "alice\x00" + text(q, "requestId")
			before := loadHandoffSnapshot(t, b)
			prior := *counts
			q["deadline"] = timestamp(o.now().Add(time.Hour))
			result, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
			if e != nil {
				t.Fatal(e)
			}
			if *counts != prior {
				t.Fatal("settlement called business code")
			}
			switch scenario {
			case "issued-missing-result":
				if result.State != "blocked" || len(result.Blocks) == 0 || result.Blocks[0].Code != "issued_permit_missing_completion" {
					t.Fatal("issued effect was cleared", result)
				}
			case "historical":
				if result.State != "historical" || result.Certainty != "completed" {
					t.Fatal("history not recognized", result)
				}
			default:
				if result.State != "settled" || result.ResolutionID == before.Journal[key].Composition.FinishID || result.PreparationID == result.ResolutionID {
					t.Fatal("terminal IDs/disposition", result)
				}
				assertSettlementPreserved(t, before, loadHandoffSnapshot(t, b), key)
				fresh, c2 := freshSettlement(t, o, b)
				again, e := fresh.SettleReadComposition(context.Background(), c2, rawJSON(t, q))
				if e != nil || again.State != "settled" || again.ResolutionID != result.ResolutionID || *counts != prior {
					t.Fatal("duplicate settlement changed work", again, e)
				}
				old := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
				if old["kind"] != "failed" || old["receiptRef"] != result.ResolutionID || *counts != prior {
					t.Fatal("old request resumed")
				}
				next := compositionRequest(fresh)
				next["expectedGeneration"] = loadHandoffSnapshot(t, b).Generation
				_, e = fresh.Handle(context.Background(), c2, rawJSON(t, next))
				if e != nil || counts.read != prior.read+1 {
					t.Fatal("distinct normal operation blocked after safe release", e, *counts)
				}
			}
			if scenario == "historical" || scenario == "issued-missing-result" {
				if !sameSnapshot(before, loadHandoffSnapshot(t, b)) {
					t.Fatal("blocked/historical call wrote state")
				}
			}
		})
	}
}
func TestB3DefaultDenyAndScope(t *testing.T) {
	for _, mode := range []string{"default", "deny", "caller", "binding", "target", "artifact", "legacy-anchor", "canceled", "descriptor-copy"} {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, q, counts := settlementFixture(t, "initial-intent", false)
			before := loadHandoffSnapshot(t, b)
			prior := *counts
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "default":
				o.settlementAuthorization = nil
			case "deny":
				o.settlementAuthorization = func(context.Context, ReadSettlementDescriptor) error { return errors.New("deny") }
			case "caller":
				ch, _ = o.AuthenticateChannel(context.Background(), "bob")
			case "binding":
				q["bindingId"] = "other"
			case "target":
				q["target"] = map[string]any{"id": "other"}
			case "artifact":
				binding := o.registry.adapters["fixture"]
				binding.ArtifactSHA256 = Hash([]byte("other"))
				o.registry.adapters["fixture"] = binding
			case "legacy-anchor":
				g, _ := b.Acquire(context.Background())
				s, _ := b.Load(context.Background(), g)
				expected := s.Expected()
				s.Journal["alice\x00"+text(q, "requestId")].Composition.AdapterArtifact = ""
				b.Commit(context.Background(), g, expected, s, "legacy-fixture")
				g.Close()
				before = loadHandoffSnapshot(t, b)
			case "canceled":
				cancel()
			case "descriptor-copy":
				o.settlementAuthorization = func(ctx context.Context, d ReadSettlementDescriptor) error {
					encoded, _ := json.Marshal(d)
					if len(encoded) > 8192 {
						t.Fatal("descriptor unbounded")
					}
					d.Plan.PhaseMaxMs["action"] = 999999
					return errors.New("deny-after-mutation")
				}
			}
			_, e := o.SettleReadComposition(ctx, ch, rawJSON(t, q))
			if e == nil {
				t.Fatal("unauthorized/missing-anchor case allowed", mode)
			}
			if *counts != prior || !sameSnapshot(before, loadHandoffSnapshot(t, b)) {
				t.Fatal("denied control wrote or acted", mode)
			}
		})
	}
}

type settlementWriteFault struct {
	Backend
	target                        string
	stored, allow, denyLater, hit bool
	id                            string
	attempts                      map[string]int
	reads                         map[string]int
	lateFailures                  int
}

func (b *settlementWriteFault) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	if b.hit && b.denyLater {
		b.lateFailures++
		return nil, fail(StorageFailure, "later-settlement-load")
	}
	return b.Backend.Load(ctx, g)
}
func (b *settlementWriteFault) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	b.attempts[id]++
	for _, j := range s.Journal {
		if j.Composition == nil || j.Composition.Settlement == nil {
			continue
		}
		r := j.Composition.Settlement
		target := r.PreparationID
		if b.target == "resolution" {
			target = r.ResolutionID
		}
		if id == target && !b.hit {
			b.hit = true
			b.id = id
			if b.stored {
				if _, err := b.Backend.Commit(ctx, g, e, s, id); err != nil {
					return CommitReceipt{}, err
				}
			}
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
		}
	}
	if b.hit && b.denyLater {
		b.lateFailures++
		return CommitReceipt{}, fail(StorageFailure, "later-settlement-commit")
	}
	return b.Backend.Commit(ctx, g, e, s, id)
}
func (b *settlementWriteFault) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	b.reads[id]++
	if id == b.id && !b.allow {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func TestB3StableTerminalIDsAcrossFailures(t *testing.T) {
	for _, fileStorage := range []bool{false, true} {
		for _, target := range []string{"preparation", "resolution"} {
			for _, stored := range []bool{false, true} {
				name := target + "/notstored"
				if stored {
					name = target + "/stored"
				}
				if fileStorage {
					name = "protected/" + name
				}
				t.Run(name, func(t *testing.T) {
					o, b, ch, q, counts := settlementStorageFixture(t, "initial-intent", false, fileStorage)
					key := "alice\x00" + text(q, "requestId")
					before := loadHandoffSnapshot(t, b)
					prep, resolution := settlementIDs(before, before.Journal[key])
					prior := *counts
					fault := &settlementWriteFault{Backend: b, target: target, stored: stored, denyLater: true, attempts: map[string]int{}, reads: map[string]int{}}
					o.backend = fault
					first, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
					if e != nil || first.State != "blocked" || !fault.hit {
						t.Fatal("terminal fault not reached", first, e)
					}
					next := compositionRequest(o)
					_, _ = o.Handle(context.Background(), ch, rawJSON(t, next))
					if fault.lateFailures == 0 {
						t.Fatal("second storage failure not reached")
					}
					fault.denyLater = false
					if fileStorage {
						file := b.(*FileBackend)
						reopened, err := NewFileBackend(FileOptions{Directory: file.options.Directory, BindingID: "fixture-binding"})
						if err != nil {
							t.Fatal(err)
						}
						fault.Backend = reopened
					}
					fresh, c2 := freshSettlement(t, o, fault)
					blocked, e := fresh.SettleReadComposition(context.Background(), c2, rawJSON(t, q))
					if e != nil || blocked.State != "blocked" || fault.reads[fault.id] < 2 || *counts != prior {
						t.Fatal("unknown transition replayed", blocked, e)
					}
					if target == "preparation" && fault.attempts[resolution] != 0 {
						t.Fatal("unconfirmed preparation attempted resolution")
					}
					fault.allow = true
					fresh, c2 = freshSettlement(t, o, fault)
					done, e := fresh.SettleReadComposition(context.Background(), c2, rawJSON(t, q))
					if e != nil || done.State != "settled" || done.PreparationID != prep || done.ResolutionID != resolution || *counts != prior {
						t.Fatal("same-ID resolution failed", done, e)
					}
					assertSettlementPreserved(t, before, loadHandoffSnapshot(t, b), key)
					fault.allow = false
					newRequest := compositionRequest(fresh)
					newRequest["expectedGeneration"] = loadHandoffSnapshot(t, b).Generation
					refused := resultMap(t)(fresh.Handle(context.Background(), c2, rawJSON(t, newRequest)))
					if refused["kind"] != "failed" || *counts != prior {
						t.Fatal("stored committed marker bypassed unknown readback")
					}
				})
			}
		}
	}
}

type stopAcquisition struct {
	Backend
	hit, allow bool
	id         string
}

func (b *stopAcquisition) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	if b.hit && !b.allow {
		return nil, fail(StorageFailure, "stop-after-intent")
	}
	return b.Backend.Load(ctx, g)
}
func (b *stopAcquisition) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	for _, j := range s.Journal {
		if r, ok := j.Intents[id]; ok && r.Step == "acquire_candidate" && r.State == "committed" && !b.hit {
			b.hit = true
			b.id = id
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
		}
	}
	return b.Backend.Commit(ctx, g, e, s, id)
}
func (b *stopAcquisition) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.id && !b.allow {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func TestB3RetainedClaimAndDistinctRecovery(t *testing.T) {
	o, b, ch, _, counts := compositionFixture(t, "recover")
	fault := &stopAcquisition{Backend: b}
	o.backend = fault
	q := compositionRequest(o)
	first := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
	if first["kind"] != "failed" || !fault.hit || counts.acquire != 0 {
		t.Fatal("claim fixture")
	}
	fault.allow = true
	o.backend = b
	o.settlementAuthorization = func(context.Context, ReadSettlementDescriptor) error { return nil }
	claim := a4Claim(t, b)
	before := loadHandoffSnapshot(t, b)
	prior := *counts
	settled, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
	if e != nil || settled.State != "settled" || len(settled.RetainedClaims) != 1 || *counts != prior {
		t.Fatal("claim ownership not settled", settled, e)
	}
	assertSettlementPreserved(t, before, loadHandoffSnapshot(t, b), "alice\x00"+text(q, "requestId"))
	fresh, c2 := freshSettlement(t, o, b)
	ordinary := compositionRequest(fresh)
	blocked := resultMap(t)(fresh.Handle(context.Background(), c2, rawJSON(t, ordinary)))
	if blocked["kind"] != "failed" || *counts != prior {
		t.Fatal("active claim fence cleared")
	}
	got := resultMap(t)(fresh.Handle(context.Background(), c2, rawJSON(t, b2Request("resume", claim.ID))))
	if got["kind"] != "recovered" || counts.read != prior.read || counts.acquire != 1 || counts.proof != 1 {
		t.Fatal("distinct authorized claim recovery blocked/replayed", got["kind"], *counts)
	}
}
func TestB3ControlExpiryAndActiveIssuer(t *testing.T) {
	for _, mode := range []string{"expiry", "active"} {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, q, counts := settlementFixture(t, "initial-intent", false)
			before := loadHandoffSnapshot(t, b)
			prior := *counts
			if mode == "active" {
				o.mu.Lock()
				for _, a := range o.active {
					a.mu.Lock()
					a.done = false
					a.mu.Unlock()
				}
				o.mu.Unlock()
			} else {
				o.settlementAuthorization = func(ctx context.Context, d ReadSettlementDescriptor) error {
					deadline, _ := ctx.Deadline()
					clock := &a2Clock{now: deadline}
					o.now = clock.Now
					return nil
				}
			}
			result, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
			if e == nil && result.State == "settled" {
				t.Fatal("expired/active control wrote")
			}
			if *counts != prior || !sameSnapshot(before, loadHandoffSnapshot(t, b)) {
				t.Fatal("control bound violated")
			}
		})
	}
}
