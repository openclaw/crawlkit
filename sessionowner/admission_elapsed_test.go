package sessionowner

import (
	"context"
	"testing"
	"time"
)

// Port of reviewer v3_closure_review_test.go successful3ms admission proof.
// Its semantic selector remains the actual intent, never new preparation.
type a4AdvanceAdmission struct {
	Backend
	clock *a2Clock
	hit   bool
}

func (b *a4AdvanceAdmission) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	current, err := b.Backend.Load(ctx, g)
	if err != nil {
		return CommitReceipt{}, err
	}
	if !b.hit && a4TransactionStage(current, s, id) == "intent" {
		b.hit = true
		b.clock.Advance(b.clock.Now().Add(3 * time.Millisecond))
	}
	return b.Backend.Commit(ctx, g, e, s, id)
}
func TestA4RPCDeadlineIncludesSuccessfulAdmission(t *testing.T) {
	clock := &a2Clock{now: time.Now().UTC().Truncate(time.Millisecond)}
	entered := false
	o, b, c := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		start := clock.Now()
		err := i.CallRPC(ctx, func(rpc context.Context) error {
			entered = true
			d, ok := rpc.Deadline()
			if !ok || !d.Equal(start.Add(5*time.Millisecond)) || clock.Now().Sub(start) != 3*time.Millisecond {
				t.Fatal("RPC deadline restarted after durable admission")
			}
			clock.Advance(d)
			if rpc.Err() != context.DeadlineExceeded || ctx.Err() != nil {
				t.Fatal("RPC/native window conflated")
			}
			return rpc.Err()
		})
		return AdapterOutput{}, err
	})
	o.now = clock.Now
	o.deadlineHook = clock.Until
	fault := &a4AdvanceAdmission{Backend: b, clock: clock}
	o.backend = fault
	k := operationKey("fixture.read", 1, "perform")
	op := cloneOperation(o.registry.operations[k])
	rpc := int64(5)
	op.BudgetPlans[0].MaxRPCMs = &rpc
	o.registry.operations[k] = op
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))))
	if !fault.hit || !entered || got["kind"] == "performed" {
		t.Fatal("actual intent/admitted RPC property not exercised")
	}
}
