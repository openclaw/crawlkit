package sessionowner

import (
	"context"
	"time"
)

// A new caller context may shorten but never extend the original private lease,
// invocation, candidate expiry or parent/channel lifetime. Cancellation is wired
// independently of AdapterSession.mu so even a pending trusted callback can stop.
func (s *AdapterSession) candidateHandoffContext(caller context.Context, expires time.Time) (context.Context, context.CancelFunc, func() error, error) {
	i := s.invocation
	deadline, e := time.Parse("2006-01-02T15:04:05.000Z", text(s.lease, "deadline"))
	if e != nil || expires.IsZero() {
		return nil, nil, nil, fail(ProtocolMismatch, "candidate_lifetime_bound")
	}
	if expires.Before(deadline) {
		deadline = expires
	}
	channel := i.active.channelRef.lifetime
	for _, ctx := range []context.Context{caller, s.parentContext, channel} {
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
	}
	base, stopBase := context.WithCancel(caller)
	stopParent := context.AfterFunc(s.parentContext, stopBase)
	stopChannel := context.AfterFunc(channel, stopBase)
	timed, stopTimer := i.owner.contextUntil(base, deadline)
	stop := func() { stopParent(); stopChannel(); stopTimer(); stopBase() }
	check := func() error {
		if !i.owner.now().Before(deadline) {
			return fail(Code("deadline_exceeded"), "candidate_lifetime_bound")
		}
		if caller.Err() != nil || timed.Err() != nil || s.parentContext.Err() != nil || channel.Err() != nil {
			return fail(Canceled, "candidate_lifetime_bound")
		}
		return nil
	}
	if e = check(); e != nil {
		stop()
		return nil, nil, nil, e
	}
	return context.WithValue(timed, candidateStorageBoundKey{}, candidateStorageBound{check: check}), stop, check, nil
}
