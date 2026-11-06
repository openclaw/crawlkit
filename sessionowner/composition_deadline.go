package sessionowner

import (
	"context"
	"time"
)

// The start is captured before parsing, approval lookup, authorization or storage.
// A later phase can consume this deadline but cannot recreate it.
func (o *Owner) admitCompositionChild(parent context.Context, root *Invocation, req *Request, plan BudgetPlan, stage string, started time.Time) (context.Context, context.CancelFunc, error) {
	if plan.MaxTotalMs <= 0 {
		return nil, nil, fail(BudgetInsufficient, "child_total")
	}
	end := started.Add(time.Duration(plan.MaxTotalMs) * time.Millisecond)
	for _, d := range []time.Time{req.Deadline, root.snapshot.Journal[root.journalKey].Composition.Deadline} {
		if d.Before(end) {
			end = d
		}
	}
	for _, ctx := range []context.Context{parent, root.active.channelRef.lifetime} {
		if d, ok := ctx.Deadline(); ok && d.Before(end) {
			end = d
		}
	}
	// The frozen wire carries millisecond deadlines. Round down, never extend.
	end = end.UTC().Truncate(time.Millisecond)
	req.Deadline = end
	req.Value["deadline"] = timestamp(end)
	timed, stop := o.contextUntil(parent, end)
	parentBound, hasParentBound := parent.Value(candidateStorageBoundKey{}).(candidateStorageBound)
	check := func() error {
		if hasParentBound {
			if e := parentBound.check(); e != nil {
				return e
			}
		}
		if !o.now().Before(end) {
			return fail(Code("deadline_exceeded"), "child_total")
		}
		if timed.Err() != nil || parent.Err() != nil || root.active.channelRef.lifetime.Err() != nil {
			return fail(Canceled, "child_lifetime")
		}
		return nil
	}
	ctx := context.WithValue(timed, candidateStorageBoundKey{}, candidateStorageBound{check: check})
	if e := o.compositionChildReserve(ctx, root, req, plan, stage); e != nil {
		stop()
		return nil, nil, e
	}
	return ctx, stop, nil
}
func (o *Owner) compositionChildReserve(ctx context.Context, root *Invocation, req *Request, p BudgetPlan, stage string) error {
	if e := checkCandidateStorageBound(ctx); e != nil {
		return e
	}
	required := p.PhaseMaxMs["commit"] + p.PhaseMaxMs["cleanup"] + p.PhaseMaxMs["ipc"]
	if stage == "recovery" {
		c := root.snapshot.Claims[text(req.Value, "claimId")]
		if c == nil || c.Candidate == nil {
			required += p.PhaseMaxMs["capture"]
			if !o.warm() && p.Readiness != "none" {
				required += p.PhaseMaxMs["startup"]
			}
		}
		if c == nil || c.Proof == nil {
			required += p.PhaseMaxMs["proof"]
		}
	} else {
		required += phaseReserve("action", p.PhaseMaxMs["action"])
		if stage == "initial" {
			required += int64(compositionRecoveryReserve(root.plan) / time.Millisecond)
		}
	}
	if required > p.MaxTotalMs || req.Deadline.Sub(o.now()) < time.Duration(required)*time.Millisecond {
		return fail(BudgetInsufficient, "child_remaining_reserve")
	}
	return nil
}
func (i *Invocation) compositionRequestContext(caller context.Context) (context.Context, context.CancelFunc) {
	end := i.Deadline()
	for _, ctx := range []context.Context{caller, i.operationCtx} {
		if d, ok := ctx.Deadline(); ok && d.Before(end) {
			end = d
		}
	}
	if ms := i.plan.MaxSingleProviderRequestMs; ms != nil {
		if d := i.owner.now().Add(time.Duration(*ms) * time.Millisecond); d.Before(end) {
			end = d
		}
	}
	base, stopBase := context.WithCancel(caller)
	stopParent := context.AfterFunc(i.operationCtx, stopBase)
	timed, stopTimer := i.owner.contextUntil(base, end)
	if i.operationCtx.Err() != nil {
		stopBase()
	}
	return timed, func() { stopParent(); stopTimer(); stopBase() }
}
