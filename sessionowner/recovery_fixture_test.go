package sessionowner

import (
	"context"
	"errors"
	"testing"
)

func candidateFenceOutcome(mode string, got map[string]any, captures, proofs int, s *Snapshot, c *Claim) bool {
	if s == nil || c == nil || captures != 1 || c.AcquisitionAttempts != 1 {
		return false
	}
	if mode == "valid-new" {
		return got["kind"] == "recovered" && proofs == 1 && !s.AuthBlocked && s.Generation != "epoch-1"
	}
	if mode != "active-rejected" && mode != "prior-rejected" {
		return false
	}
	return got["kind"] == "failed" && proofs == 0 && s.Generation == "epoch-1" && (mode != "active-rejected" || s.AuthBlocked)
}
func TestA3RejectionAssertionsRejectOldRecoveredOutcome(t *testing.T) {
	old := map[string]any{"kind": "recovered"}
	s := NewSnapshot("fixture-binding")
	s.Generation = "replaced-epoch"
	c := &Claim{AcquisitionAttempts: 1}
	for _, mode := range []string{"active-rejected", "prior-rejected"} {
		if candidateFenceOutcome(mode, old, 1, 1, s, c) {
			t.Fatal("old reproduced guard bypass passes corrected assertion", mode)
		}
	}
}

type a3ExpireCleanup struct {
	Backend
	clock  *a2Clock
	forced bool
}

func (b *a3ExpireCleanup) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	if !b.forced {
		for _, c := range s.Claims {
			if c != nil && c.Phase == "EFFECT_UNCERTAIN" {
				b.forced = true
				deadline, ok := ctx.Deadline()
				if !ok {
					return CommitReceipt{}, errors.New("missing cleanup deadline")
				}
				b.clock.Advance(deadline)
				return CommitReceipt{OperationID: id, Certainty: "uncertain"}, ctx.Err()
			}
		}
	}
	return b.Backend.Commit(ctx, g, e, s, id)
}
func TestA3ExplicitCleanupExpiryWithholdsUnconfirmedClaim(t *testing.T) {
	effects := 0
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
			return AdapterOutput{}, e
		}
		effects++
		return AdapterOutput{}, errors.New("synthetic lost capture")
	})
	clock := &a2Clock{now: o.now()}
	o.now = clock.Now
	o.deadlineHook = clock.Until
	fault := &a3ExpireCleanup{Backend: b, clock: clock}
	o.backend = fault
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	failure, _ := object(got["failure"])
	if !fault.forced || effects != 1 || got["kind"] != "failed" || got["certainty"] != "uncertain" || got["claim"] != nil || failure["category"] != "storage_failure" {
		t.Fatal("expired cleanup was hidden or unconfirmed state returned")
	}
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	for _, cl := range s.Claims {
		if cl.AcquisitionAttempts != 1 || len(cl.EffectIDs) != 1 {
			t.Fatal("issued acquisition refunded after cleanup expiry")
		}
	}
}
