//go:build darwin

package sessionowner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type controlMatrixBackend struct {
	Backend
	before                                 func(context.Context, string)
	after                                  func(context.Context, string)
	acquires                               atomic.Int32
	loads, commits, reconciles, lateWrites int
	entered                                chan int
	expired                                func() bool
}

func (b *controlMatrixBackend) Acquire(ctx context.Context) (*Guard, error) {
	n := int(b.acquires.Add(1))
	if b.entered != nil {
		b.entered <- n
	}
	return b.Backend.Acquire(ctx)
}
func (b *controlMatrixBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	b.loads++
	if b.before != nil {
		b.before(ctx, "load")
	}
	s, e := b.Backend.Load(ctx, g)
	if b.after != nil {
		b.after(ctx, "load")
	}
	return s, e
}
func (b *controlMatrixBackend) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	stage := "commit"
	for _, j := range s.Journal {
		if j.Composition == nil || j.Composition.Settlement == nil {
			continue
		}
		r := j.Composition.Settlement
		if id == r.PreparationID {
			stage = "prepare"
		}
		if id == r.ResolutionID {
			stage = "resolve"
		}
	}
	if b.expired != nil && b.expired() {
		b.lateWrites++
	}
	b.commits++
	if b.before != nil {
		b.before(ctx, stage)
	}
	receipt, err := b.Backend.Commit(ctx, g, e, s, id)
	if b.after != nil {
		b.after(ctx, stage)
	}
	return receipt, err
}
func (b *controlMatrixBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	b.reconciles++
	if b.before != nil {
		b.before(ctx, "reconcile")
	}
	r, e := b.Backend.Reconcile(ctx, g, id)
	if b.after != nil {
		b.after(ctx, "reconcile")
	}
	return r, e
}

type controlMatrixAuth struct {
	fixtureAuth
	allow func(context.Context, int)
	calls int
}

func (a *controlMatrixAuth) Allow(ctx context.Context, c string, op Operation, v map[string]any) error {
	a.calls++
	if a.allow != nil {
		a.allow(ctx, a.calls)
	}
	return a.fixtureAuth.Allow(ctx, c, op, v)
}
func TestB3aControlCancellationWindows(t *testing.T) {
	for _, point := range []string{"allow-current", "allow-original", "capability", "prepare", "resolve", "load", "reconcile", "status"} {
		for _, mode := range []string{"caller", "channel", "expiry"} {
			t.Run(point+"/"+mode, func(t *testing.T) {
				o, b, ch, q, counts := settlementFixture(t, "initial-intent", false)
				before := loadHandoffSnapshot(t, b)
				prior := *counts
				clock := &a2Clock{now: o.now()}
				o.now = clock.Now
				o.deadlineHook = clock.Until
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				hit := false
				var control context.Context
				trigger := func() {
					if hit {
						return
					}
					hit = true
					switch mode {
					case "caller":
						cancel()
					case "channel":
						ch.Close()
					case "expiry":
						d, ok := control.Deadline()
						if !ok {
							t.Fatal("control deadline absent")
						}
						clock.Advance(d)
					}
				}
				auth := &controlMatrixAuth{allow: func(c context.Context, n int) {
					control = c
					if point == "allow-current" && n == 1 || point == "allow-original" && n == 2 {
						trigger()
					}
				}}
				o.auth = auth
				o.settlementAuthorization = func(c context.Context, d ReadSettlementDescriptor) error {
					control = c
					if point == "capability" {
						trigger()
					}
					return nil
				}
				wrapper := &controlMatrixBackend{Backend: b, expired: func() bool { return hit }}
				wrapper.after = func(c context.Context, stage string) {
					control = c
					if stage == point {
						trigger()
					}
				}
				o.backend = wrapper
				o.compositionBoundary = func(stage string) {
					if point == "status" && stage == "settlement_status" {
						trigger()
					}
				}
				got, e := o.SettleReadComposition(ctx, ch, rawJSON(t, q))
				if !hit || got.State == "settled" || got.State == "historical" || *counts != prior || wrapper.lateWrites != 0 {
					t.Fatal("control boundary violation", point, mode, hit, got, e, wrapper.lateWrites)
				}
				after := loadHandoffSnapshot(t, b)
				if after.Journal["alice\x00"+text(q, "requestId")].Composition.Settlement == nil {
					if !sameSnapshot(before, after) {
						t.Fatal("refusal wrote unrelated metadata")
					}
				} else {
					assertSettlementPreserved(t, before, after, "alice\x00"+text(q, "requestId"))
				}
			})
		}
	}
}
func TestB3aHeldMemoryAndProtectedGuard(t *testing.T) {
	for _, file := range []bool{false, true} {
		storage := "memory"
		if file {
			storage = "protected"
		}
		for _, mode := range []string{"queue", "caller", "channel"} {
			t.Run(storage+"/"+mode, func(t *testing.T) {
				o, b, ch, q, counts := settlementStorageFixture(t, "initial-intent", false, file)
				prior := *counts
				clock := &a2Clock{now: o.now()}
				o.now = clock.Now
				o.deadlineHook = clock.Until
				held, e := b.Acquire(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				defer held.Close()
				var contender Backend = b
				if file {
					fb := b.(*FileBackend)
					other, e := NewFileBackend(FileOptions{Directory: fb.options.Directory, BindingID: "fixture-binding"})
					if e != nil {
						t.Fatal(e)
					}
					contender = other
				}
				entered := make(chan int, 1)
				wrapper := &controlMatrixBackend{Backend: contender, entered: entered}
				o.backend = wrapper
				caps := 0
				o.settlementAuthorization = func(context.Context, ReadSettlementDescriptor) error { caps++; return nil }
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					s, e := o.SettleReadComposition(ctx, ch, rawJSON(t, q))
					if s.State != "" {
						done <- errors.New("held guard returned status")
						return
					}
					done <- e
				}()
				<-entered
				switch mode {
				case "queue":
					clock.Advance(clock.Now().Add(250 * time.Millisecond))
				case "caller":
					cancel()
				case "channel":
					ch.Close()
				}
				select {
				case e := <-done:
					if ErrorCode(e) != OwnerBusy {
						t.Fatal("queue did not refuse", e)
					}
				case <-time.After(time.Second):
					t.Fatal("held guard control did not stop")
				}
				held.Close()
				if wrapper.loads != 0 || wrapper.commits != 0 || caps != 0 || *counts != prior {
					t.Fatal("held guard bypassed")
				}
			})
		}
	}
}
func TestB3aConcurrentControlsOneSequence(t *testing.T) {
	for _, file := range []bool{false, true} {
		name := "memory"
		if file {
			name = "protected"
		}
		t.Run(name, func(t *testing.T) {
			o, b, ch, q, counts := settlementStorageFixture(t, "initial-intent", false, file)
			prior := *counts
			before := loadHandoffSnapshot(t, b)
			entered := make(chan int, 3)
			wrapper := &controlMatrixBackend{Backend: b, entered: entered}
			o.backend = wrapper
			capEntered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			o.settlementAuthorization = func(context.Context, ReadSettlementDescriptor) error {
				if calls.Add(1) == 1 {
					close(capEntered)
					<-release
				}
				return nil
			}
			type answer struct {
				s ReadSettlementStatus
				e error
			}
			done := make(chan answer, 2)
			go func() { s, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q)); done <- answer{s, e} }()
			<-capEntered
			<-entered
			second, _ := o.AuthenticateChannel(context.Background(), "alice")
			go func() {
				s, e := o.SettleReadComposition(context.Background(), second, rawJSON(t, q))
				done <- answer{s, e}
			}()
			<-entered
			close(release)
			a, c := <-done, <-done
			if a.e != nil || c.e != nil || a.s.State != "settled" || c.s.State != "settled" || a.s.PreparationID != c.s.PreparationID || a.s.ResolutionID != c.s.ResolutionID || wrapper.commits != 2 || calls.Load() != 2 || *counts != prior {
				t.Fatal("concurrent controls diverged", a, c, wrapper.commits)
			}
			assertSettlementPreserved(t, before, loadHandoffSnapshot(t, b), "alice\x00"+text(q, "requestId"))
			fresh, c3 := freshSettlement(t, o, b)
			again, e := fresh.SettleReadComposition(context.Background(), c3, rawJSON(t, q))
			if e != nil || again.ResolutionID != a.s.ResolutionID || *counts != prior {
				t.Fatal("fresh duplicate changed sequence")
			}
		})
	}
}
func TestB3aClaimCauseOriginAndIndependentFences(t *testing.T) {
	for _, mode := range []string{"cause", "diagnosis", "origin", "identity", "independent"} {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, q, counts := settlementFixture(t, "retry-intent", false)
			g, _ := b.Acquire(context.Background())
			s, _ := b.Load(context.Background(), g)
			expected := s.Expected()
			var claim *Claim
			for _, c := range s.Claims {
				claim = c
			}
			if claim == nil {
				t.Fatal("claim setup")
			}
			switch mode {
			case "cause":
				claim.Cause = "rejected_generation"
			case "diagnosis":
				claim.DiagnosisID = "unknown-original-diagnosis"
			case "origin":
				j := s.Journal[claim.OriginJournalKey]
				j.OperationID = "foreign.recovery"
				for id, r := range j.Intents {
					r.OperationID = j.OperationID
					j.Intents[id] = r
				}
			case "identity":
				s.Identity = map[string]any{"account": "other"}
			case "independent":
				s.Journal["alice\x00independent"] = &JournalEntry{Caller: "alice", RequestID: "independent", RequestKind: "perform", Generation: s.Generation, Semantics: "safe_read", Certainty: "uncertain"}
			}
			if recoveryConsistency(s) != nil {
				t.Fatal("negative missed intended valid shape")
			}
			if _, e := b.Commit(context.Background(), g, expected, s, "scope-fixture"); e != nil {
				t.Fatal(e)
			}
			g.Close()
			before := loadHandoffSnapshot(t, b)
			prior := *counts
			status, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
			if mode != "independent" {
				if e == nil || status.State != "" || !sameSnapshot(before, loadHandoffSnapshot(t, b)) {
					t.Fatal("bad claim/identity anchor accepted", mode, status, e)
				}
			} else {
				if e != nil || status.State != "settled" {
					t.Fatal("independent fence prevented scoped metadata", status, e)
				}
				fresh, c2 := freshSettlement(t, o, b)
				next := compositionRequest(fresh)
				next["expectedGeneration"] = before.Generation
				got := resultMap(t)(fresh.Handle(context.Background(), c2, rawJSON(t, next)))
				if got["kind"] != "failed" {
					t.Fatal("independent uncertainty cleared")
				}
			}
			if *counts != prior {
				t.Fatal("scope test dispatched business")
			}
		})
	}
}

func TestB3aMultipleIndependentClaimsRemainFenced(t *testing.T) {
	o, b, ch, q, counts := settlementFixture(t, "initial-intent", false)
	first := b2Seed(t, o, b)
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	expected := s.Expected()
	second := *s.Claims[first.ID]
	second.ID = "independent-second"
	second.Cause = "rejected_generation"
	second.OriginRequestID = "second-origin"
	second.OriginJournalKey = "alice\x00second-origin"
	s.Claims[second.ID] = &second
	j := *s.Journal[first.OriginJournalKey]
	j.ClaimID = second.ID
	j.RequestID = second.OriginRequestID
	s.Journal[second.OriginJournalKey] = &j
	if e := recoveryConsistency(s); e != nil {
		t.Fatal("multiple claim shape", e)
	}
	if _, e := b.Commit(context.Background(), g, expected, s, "multiple-claim-fixture"); e != nil {
		t.Fatal(e)
	}
	g.Close()
	before := loadHandoffSnapshot(t, b)
	prior := *counts
	status, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
	if e != nil || status.State != "settled" || *counts != prior {
		t.Fatal("scoped ownership did not settle", status, e)
	}
	after := loadHandoffSnapshot(t, b)
	assertSettlementPreserved(t, before, after, "alice\x00"+text(q, "requestId"))
	available, constraint := o.settlementRecoveryRoute(after, "alice", first.ID)
	if !available || constraint != "independent_claim_or_foreign_intent_fence" {
		t.Fatal("method presence misreported as permission", available, constraint)
	}
	fresh, c2 := freshSettlement(t, o, b)
	got := resultMap(t)(fresh.Handle(context.Background(), c2, rawJSON(t, b2Request("resume", first.ID))))
	if got["kind"] != "failed" || *counts != prior {
		t.Fatal("independent active claim bypassed")
	}
}
