package sessionowner

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type b3bWaitEvent struct {
	Context  context.Context
	Entered  time.Time
	Deadline time.Time
}

// This wrapper observes the real MemoryBackend gate, not a second semaphore.
// The failed nonblocking take is the contention witness; the original queue
// context drives the blocking wait and its existing OwnerBusy result.
type b3bQueueBackend struct {
	*MemoryBackend
	t                                                   *testing.T
	acquires, loads, commits, waiting, guards, releases atomic.Int32
	events                                              chan b3bWaitEvent
}

func (b *b3bQueueBackend) guard() *Guard {
	b.guards.Add(1)
	return NewGuard(b.MemoryBackend, func() error { b.guards.Add(-1); b.releases.Add(1); b.gate <- struct{}{}; return nil })
}
func (b *b3bQueueBackend) Acquire(ctx context.Context) (*Guard, error) {
	b.acquires.Add(1)
	entered := time.Now()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Sub(entered) > time.Second {
		b.t.Error("ordinary queue deadline missing or raised")
	}
	select {
	case <-b.gate:
		return b.guard(), nil
	default:
	}
	b.waiting.Add(1)
	defer b.waiting.Add(-1)
	b.events <- b3bWaitEvent{ctx, entered, deadline}
	select {
	case <-ctx.Done():
		return nil, fail(OwnerBusy, "lock")
	case <-b.gate:
		return b.guard(), nil
	}
}
func (b *b3bQueueBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	b.loads.Add(1)
	return b.MemoryBackend.Load(ctx, g)
}
func (b *b3bQueueBackend) Commit(ctx context.Context, g *Guard, x Expected, s *Snapshot, id string) (CommitReceipt, error) {
	b.commits.Add(1)
	return b.MemoryBackend.Commit(ctx, g, x, s, id)
}
func b3bWait(t *testing.T, b *b3bQueueBackend) b3bWaitEvent {
	t.Helper()
	select {
	case e := <-b.events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("waiting Acquire handshake absent")
		return b3bWaitEvent{}
	}
}
func b3bUnfinished(o *Owner) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, a := range o.active {
		a.mu.Lock()
		if !a.done {
			n++
		}
		a.mu.Unlock()
	}
	return n
}
func b3bDone(t *testing.T, o *Owner, ch *Channel, id string) bool {
	t.Helper()
	o.mu.Lock()
	a := o.active[activeKey(ch, id)]
	o.mu.Unlock()
	if a == nil {
		t.Fatal("expected actual active entry absent")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.done
}
func b3bAssertOrdinaryHistory(t *testing.T, b *MemoryBackend, ids ...string) {
	t.Helper()
	s := loadHandoffSnapshot(t, b)
	if len(s.Journal) != len(ids) || s.Generation != "epoch-1" {
		t.Fatal("unexpected ordinary journal/epoch count")
	}
	for _, id := range ids {
		j := s.Journal["alice\x00"+id]
		if j == nil || j.Dispatches != 1 || j.Certainty != "completed" || j.UnresolvedIntentID != "" || len(j.EffectIDs) != 1 || len(j.Intents) != 1 {
			t.Fatal("ordinary permit/history mismatch", id)
		}
	}
}

func TestB2C3BOrdinaryQueue(t *testing.T) {
	for _, name := range []string{"B11_ordinary_queue_17th", "B12_canceled_waiter_reusable", "B13_closed_channel_waiter", "B14_ordinary_queue_timeout", "B29_cancel_cross_caller", "B30_cancel_cross_binding"} {
		t.Run(name, func(t *testing.T) {
			group := b3bRuns(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			group.releases = append(group.releases, unblock)
			var effects atomic.Int32
			var heldCtx context.Context
			var heldGuard *Guard
			var atHold *Snapshot
			o, base, _, ch, _ := b3bFixture(t, "fixture-binding", "fixture-reader", "queue-private-context", func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
					return AdapterOutput{}, e
				}
				effects.Add(1)
				id := text(i.request.Value, "requestId")
				if id == "holder" {
					heldCtx, heldGuard = ctx, i.guard
					var e error
					atHold, e = i.snapshot.Clone()
					if e != nil {
						return AdapterOutput{}, e
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return AdapterOutput{}, ctx.Err()
					}
				}
				return AdapterOutput{Result: map[string]any{"value": id + "-public"}}, nil
			})
			b := &b3bQueueBackend{MemoryBackend: base, t: t, events: make(chan b3bWaitEvent, 17)}
			o.backend = b
			if name == "B14_ordinary_queue_timeout" {
				guard, e := base.Acquire(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				var guardOnce sync.Once
				releaseGuard := func() { guardOnce.Do(func() { guard.Close() }) }
				group.releases = append(group.releases, releaseGuard)
				q := b3bRequest("fixture-binding", "queue-timeout")
				before := loadHandoffSnapshotUnderGuardB3B(t, base, guard)
				done, _ := group.start(o, ch, b3bValid(t, o, q))
				event := b3bWait(t, b)
				result := b3bAwait(t, done)
				b3bFailed(t, result.raw, result.err, OwnerBusy)
				if event.Context.Err() != context.DeadlineExceeded || event.Deadline.Sub(event.Entered) > time.Second || b.loads.Load() != 0 || b.commits.Load() != 0 || effects.Load() != 0 || b.waiting.Load() != 0 || guard.Check(base) != nil || !sameSnapshot(before, loadHandoffSnapshotUnderGuardB3B(t, base, guard)) {
					t.Fatal("real queue timeout changed state/guard")
				}
				if !b3bDone(t, o, ch, "queue-timeout") {
					t.Fatal("timed out entry unfinished")
				}
				releaseGuard()
				t.Logf("B3B_STAGE real queue deadline reached after %s; load/commit/effects=0, native guard retained", time.Since(event.Entered))
				return
			}
			// Prepare all requested wires before holding the gate, so fixture setup does
			// not consume the waiters' real one-second queue windows.
			holderWire := b3bValid(t, o, b3bRequest("fixture-binding", "holder"))
			waiterWire := b3bValid(t, o, b3bRequest("fixture-binding", "waiter"))
			thirdWire := b3bValid(t, o, b3bRequest("fixture-binding", "third"))
			batch := make([][]byte, 15)
			if name == "B11_ordinary_queue_17th" {
				for n := range batch {
					batch[n] = b3bValid(t, o, b3bRequest("fixture-binding", fmt.Sprintf("waiter-%02d", n)))
				}
			}
			holderDone, _ := group.start(o, ch, holderWire)
			b3bSignal(t, entered)
			if b.guards.Load() != 1 || effects.Load() != 1 || heldCtx.Err() != nil {
				t.Fatal("holder not live under real guard")
			}
			if name == "B11_ordinary_queue_17th" {
				done := make([]<-chan b3bCallResult, 15)
				cancel := make([]context.CancelFunc, 15)
				for n := range done {
					done[n], cancel[n] = group.start(o, ch, batch[n])
				}
				events := make([]b3bWaitEvent, 15)
				for n := range events {
					events[n] = b3bWait(t, b)
				}
				if b.acquires.Load() != 16 || b.waiting.Load() != 15 || b.guards.Load() != 1 || b3bUnfinished(o) != 16 {
					t.Fatal("required 1holder+15waiting+16unfinished shape not reached")
				}
				for _, event := range events {
					if event.Context.Err() != nil {
						t.Fatal("waiter expired before saturation probe")
					}
				}
				loads, commits := b.loads.Load(), b.commits.Load()
				overflow := b3bRequest("fixture-binding", "overflow-17")
				out, e := o.Handle(context.Background(), ch, b3bValid(t, o, overflow))
				b3bFailed(t, out, e, OwnerBusy)
				if b.acquires.Load() != 16 || b.loads.Load() != loads || b.commits.Load() != commits || b3bUnfinished(o) != 16 || effects.Load() != 1 {
					t.Fatal("17th request reached Acquire or shape collapsed")
				}
				for _, event := range events {
					if event.Context.Err() != nil {
						t.Fatal("saturation probe lost a live waiter")
					}
				}
				for _, stop := range cancel {
					stop()
				}
				for _, wait := range done {
					result := b3bAwait(t, wait)
					b3bFailed(t, result.raw, result.err, OwnerBusy)
				}
				if b.waiting.Load() != 0 || b.loads.Load() != loads || b.commits.Load() != commits || b3bUnfinished(o) != 1 {
					t.Fatal("canceled waiters loaded/wrote or remained active")
				}
				unblock()
				result := b3bAwait(t, holderDone)
				if resultMap(t)(result.raw, result.err)["kind"] != "performed" {
					t.Fatal("holder failed")
				}
				b3bAssertOrdinaryHistory(t, base, "holder")
				if effects.Load() != 1 || b.guards.Load() != 0 || b.releases.Load() != 1 || b3bUnfinished(o) != 0 {
					t.Fatal("saturation cleanup not exact")
				}
				t.Log("B3B_STAGE 16 actual unfinished requests,15 failed real-gate acquisitions;17th refused before Acquire;all15 canceled/settled before holder release")
				return
			}
			if name == "B12_canceled_waiter_reusable" || name == "B13_closed_channel_waiter" {
				waiterCh := ch
				if name == "B13_closed_channel_waiter" {
					var e error
					waiterCh, e = o.AuthenticateChannel(context.Background(), "alice")
					if e != nil {
						t.Fatal(e)
					}
				}
				loads, commits := b.loads.Load(), b.commits.Load()
				waiterDone, stop := group.start(o, waiterCh, waiterWire)
				event := b3bWait(t, b)
				if b.waiting.Load() != 1 || b3bUnfinished(o) != 2 || event.Context.Err() != nil {
					t.Fatal("waiter not actually queued")
				}
				if name == "B12_canceled_waiter_reusable" {
					stop()
				} else {
					waiterCh.Close()
				}
				result := b3bAwait(t, waiterDone)
				b3bFailed(t, result.raw, result.err, OwnerBusy)
				if event.Context.Err() != context.Canceled || b.loads.Load() != loads || b.commits.Load() != commits || effects.Load() != 1 || !b3bDone(t, o, waiterCh, "waiter") || b3bUnfinished(o) != 1 {
					t.Fatal("canceled/closed waiter touched storage/effect or slot")
				}
				nextCh := ch
				nextWire := thirdWire
				nextID := "third"
				if name == "B13_closed_channel_waiter" {
					acquires := b.acquires.Load()
					out, e := o.Handle(context.Background(), waiterCh, thirdWire)
					if e == nil || ErrorCode(e) != PermissionDenied || len(out) != 0 || b.acquires.Load() != acquires {
						t.Fatal("closed channel revived")
					}
					nextCh, e = o.AuthenticateChannel(context.Background(), "alice")
					if e != nil {
						t.Fatal(e)
					}
					nextWire = waiterWire
					nextID = "waiter"
				}
				nextDone, _ := group.start(o, nextCh, nextWire)
				nextEvent := b3bWait(t, b)
				if nextEvent.Context.Err() != nil || b.waiting.Load() != 1 || b3bUnfinished(o) != 2 {
					t.Fatal("fresh request did not acquire reusable unfinished slot")
				}
				unblock()
				heldResult := b3bAwait(t, holderDone)
				nextResult := b3bAwait(t, nextDone)
				if resultMap(t)(heldResult.raw, heldResult.err)["kind"] != "performed" || resultMap(t)(nextResult.raw, nextResult.err)["kind"] != "performed" {
					t.Fatal("holder/fresh request did not complete")
				}
				b3bAssertOrdinaryHistory(t, base, "holder", nextID)
				if effects.Load() != 2 || b.guards.Load() != 0 || b.waiting.Load() != 0 || b3bUnfinished(o) != 0 {
					t.Fatal("reusable queue cleanup")
				}
				t.Log("B3B_STAGE actual waiter canceled/closed with0loads/writes/effects;fresh request completed with separate exact permit")
				return
			}
			// The held operation has already committed its single effect permit. Foreign
			// cancellation must not alter its context, durable history or receipt.
			q := fixtureRequest("cancel")
			q["requestId"] = "foreign-cancel"
			q["targetRequestId"] = "holder"
			q["reason"] = "caller_request"
			cancelCh := ch
			if name == "B29_cancel_cross_caller" {
				var e error
				cancelCh, e = o.AuthenticateChannel(context.Background(), "bob")
				if e != nil {
					t.Fatal(e)
				}
				b3bValid(t, o, q)
			} else {
				b3bValid(t, o, q)
				q["bindingId"] = "foreign-binding"
				if e := o.registry.schemas.Validate(schemaPrefix+"cancel", normalize(q)); e != nil {
					t.Fatal("wrong binding cancel shape invalid", e)
				}
			}
			counts := [3]int32{b.acquires.Load(), b.loads.Load(), b.commits.Load()}
			out, e := o.Handle(context.Background(), cancelCh, rawJSON(t, q))
			if name == "B29_cancel_cross_caller" {
				ack := resultMap(t)(out, e)
				if ack["disposition"] != "not_found" || ack["certainty"] != nil || ack["outcomeRef"] != nil {
					t.Fatal("cross-caller cancellation reflected/canceled original")
				}
			} else if e == nil || ErrorCode(e) != PermissionDenied || len(out) != 0 {
				t.Fatal("cross-binding cancellation admitted")
			}
			if heldCtx.Err() != nil || counts != [3]int32{b.acquires.Load(), b.loads.Load(), b.commits.Load()} || !sameSnapshot(atHold, loadHandoffSnapshotUnderGuardB3B(t, base, heldGuard)) {
				t.Fatal("foreign cancellation changed held operation/history")
			}
			originalReceipt := atHold.Journal["alice\x00holder"].ReceiptID
			unblock()
			result := b3bAwait(t, holderDone)
			got := resultMap(t)(result.raw, result.err)
			if got["kind"] != "performed" || got["certainty"] != "completed" || got["receiptRef"] != originalReceipt || effects.Load() != 1 {
				t.Fatal("foreign cancel changed outcome/receipt")
			}
			b3bAssertOrdinaryHistory(t, base, "holder")
			if b.guards.Load() != 0 || b3bUnfinished(o) != 0 {
				t.Fatal("cancel fixture not settled")
			}
			t.Log("B3B_STAGE foreign cancel refused;original held context/history unchanged,original receipt completed once")
		})
	}
}
func loadHandoffSnapshotUnderGuardB3B(t *testing.T, b Backend, g *Guard) *Snapshot {
	t.Helper()
	s, e := b.Load(context.Background(), g)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
