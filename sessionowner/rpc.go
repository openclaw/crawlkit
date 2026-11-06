package sessionowner

import (
	"context"
	"time"
)

// RPCContext is for a designated RPC call, never the entire native operation.
// The registry's RPC ceiling nests inside the effective phase and outer deadline.
// A nil maxRpcMs means phase-only for a private host with no separate RPC budget.
func (i *Invocation) RPCContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := i.Deadline()
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if ms := i.plan.MaxRPCMs; ms != nil {
		if d := i.owner.now().Add(time.Duration(*ms) * time.Millisecond); d.Before(deadline) {
			deadline = d
		}
	}
	return i.owner.contextUntil(ctx, deadline)
}

// CallRPC is an explicit trusted-adapter RPC seam with durable before-effect
// admission. SDK/provider code remains outside this package.
func (i *Invocation) CallRPC(ctx context.Context, call func(context.Context) error) error {
	if call == nil || i.plan.MaxRPCMs == nil {
		return fail(ProtocolMismatch, "rpc_policy")
	}
	rpcCtx, cancel := i.RPCContext(ctx)
	defer cancel()
	phase := map[string]string{"execute": "EXECUTING", "readiness": "WAIT_READINESS", "acquire_candidate": "ACQUIRING", "prove_candidate": "PROVING", "reconcile": "RECONCILING"}[i.step]
	if err := i.BeforeEffect(rpcCtx, phase); err != nil {
		return err
	}
	err := call(rpcCtx)
	if rpcCtx.Err() != nil {
		return fail(Code("deadline_exceeded"), "rpc_deadline")
	}
	return err
}

func (o *Owner) contextUntil(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if o.deadlineHook != nil {
		return o.deadlineHook(ctx, deadline)
	}
	return context.WithDeadline(ctx, deadline)
}
