package sessionowner

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Owner-local deterministic timer; no global runtime/config override. Production
// uses context.WithDeadline unchanged, including the original pre-admission start.
type a2Deadline struct {
	context.Context
	at      time.Time
	expired atomic.Bool
	cancel  context.CancelFunc
}

func (c *a2Deadline) Deadline() (time.Time, bool) { return c.at, true }
func (c *a2Deadline) Err() error {
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

type a2Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*a2Deadline
}

func (c *a2Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *a2Clock) Until(parent context.Context, d time.Time) (context.Context, context.CancelFunc) {
	if p, ok := parent.Deadline(); ok && p.Before(d) {
		d = p
	}
	ctx, cancel := context.WithCancel(parent)
	timer := &a2Deadline{Context: ctx, at: d, cancel: cancel}
	c.mu.Lock()
	c.timers = append(c.timers, timer)
	expired := !d.After(c.now)
	c.mu.Unlock()
	if expired {
		timer.expired.Store(true)
		cancel()
	}
	return timer, cancel
}
func (c *a2Clock) Advance(at time.Time) {
	c.mu.Lock()
	if at.Before(c.now) {
		c.mu.Unlock()
		panic("clock backwards")
	}
	c.now = at
	timers := append([]*a2Deadline(nil), c.timers...)
	c.mu.Unlock()
	for _, timer := range timers {
		if !timer.at.After(at) {
			timer.expired.Store(true)
			timer.cancel()
		}
	}
}
func testA2ControlledDeadlineProperties(t *testing.T) {
	for _, shortPhase := range []bool{false, true} {
		name := "rpc-shorter"
		if shortPhase {
			name = "phase-shorter"
		}
		t.Run(name, func(t *testing.T) {
			clock := &a2Clock{now: time.Now().UTC().Truncate(time.Millisecond)}
			nativeAlive, entered, canceled := false, false, false
			o, _, ch := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				time.Sleep(20 * time.Millisecond)
				nativeAlive = ctx.Err() == nil
				if !nativeAlive {
					t.Fatal("RPC cap globally clamped native work")
				}
				if shortPhase {
					i.effectiveDeadline = clock.Now().Add(3 * time.Millisecond)
				}
				expected := clock.Now().Add(5 * time.Millisecond)
				if i.Deadline().Before(expected) {
					expected = i.Deadline()
				}
				session, e := NewAdapterSession(i, "controlled-lifetime")
				if e != nil {
					t.Fatal("session construction", e)
				}
				frame, e := session.InvokeFrame()
				if e != nil {
					t.Fatal("private admission", e)
				}
				v, e := DecodeJSON(frame, HardFrameLimit)
				if e != nil {
					t.Fatal(e)
				}
				m, _ := object(v)
				budget, _ := object(m["budget"])
				lease, _ := object(m["lease"])
				if text(budget, "phaseDeadline") != timestamp(expected) || text(lease, "deadline") != timestamp(expected) {
					t.Fatal("overstated or missing private RPC/phase deadline")
				}
				err := i.CallRPC(ctx, func(rpc context.Context) error {
					entered = true
					d, ok := rpc.Deadline()
					if !ok || !d.Equal(expected) {
						t.Fatal("missing RPC clamp")
					}
					clock.Advance(expected)
					canceled = rpc.Err() == context.DeadlineExceeded
					if !canceled || ctx.Err() != nil {
						t.Fatal("RPC cancellation did not remain separate from native context")
					}
					return rpc.Err()
				})
				if !entered || !canceled || ErrorCode(err) != "deadline_exceeded" {
					t.Fatal("admitted RPC did not expire exactly", err)
				}
				before := i.effects
				cp := map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": session.lease, "sequence": "0", "phase": "EXECUTING", "event": "before_effect", "effectCertainty": "not_dispatched", "evidenceId": "expired-checkpoint", "evidence": map[string]any{"stage": "synthetic-before-effect"}}
				ack, _, e := session.Accept(ctx, rawJSON(t, cp))
				if e == nil || len(ack) != 0 || i.effects != before {
					t.Fatal("expired lease allowed ACK/effect")
				}
				return AdapterOutput{}, err
			})
			o.now = clock.Now
			o.deadlineHook = clock.Until
			key := operationKey("fixture.read", 1, "perform")
			op := cloneOperation(o.registry.operations[key])
			rpc := int64(5)
			op.BudgetPlans[0].MaxRPCMs = &rpc
			o.registry.operations[key] = op
			result := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, fixtureRequest("perform"))))
			if result["kind"] == "performed" || !nativeAlive || !entered || !canceled {
				t.Fatal("controlled deadline properties not exercised")
			}
		})
	}
}

type a2ExpireAdmission struct {
	Backend
	clock   *a2Clock
	expired bool
}

func (b *a2ExpireAdmission) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	if !b.expired {
		for _, j := range s.Journal {
			if j.Phase == "EXECUTING" && j.Dispatches > 0 {
				b.expired = true
				d, _ := ctx.Deadline()
				b.clock.Advance(d)
				return CommitReceipt{OperationID: id, Certainty: "not_committed"}, nil
			}
		}
	}
	return b.Backend.Commit(ctx, g, e, s, id)
}
func TestA2RPCAdmissionExpiryHasNoCallback(t *testing.T) {
	clock := &a2Clock{now: time.Now().UTC().Truncate(time.Millisecond)}
	entered := false
	o, b, ch := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		err := i.CallRPC(ctx, func(context.Context) error { entered = true; return nil })
		if err == nil {
			t.Fatal("expired admission succeeded")
		}
		return AdapterOutput{}, err
	})
	o.now = clock.Now
	o.deadlineHook = clock.Until
	o.backend = &a2ExpireAdmission{Backend: b, clock: clock}
	key := operationKey("fixture.read", 1, "perform")
	op := cloneOperation(o.registry.operations[key])
	rpc := int64(5)
	op.BudgetPlans[0].MaxRPCMs = &rpc
	o.registry.operations[key] = op
	result := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, fixtureRequest("perform"))))
	if entered || result["kind"] == "performed" {
		t.Fatal("callback after pre-dispatch expiry")
	}
}
