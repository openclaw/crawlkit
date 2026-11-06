package sessionowner

import "context"

// A fresh, exact same-claim resume may resolve an uncertainty marker locally.
// Definite non-commit does not refund a permit; it proves the proposed permit
// was never installed. A committed witness is retained and routed to the existing
// effect reconciler, never to acquisition again.
func (o *Owner) reconcileUnresolvedIntents(ctx context.Context, i *Invocation) error {
	if e := o.reconcilePreparedIntents(ctx, i); e != nil {
		return e
	}
	ids := []string{}
	for _, j := range i.snapshot.Journal {
		if j.UnresolvedIntentID != "" {
			if j.ClaimID != i.claim.ID || j.Caller != i.claim.Caller || j.Generation != i.claim.Epoch || j.RequestKind != "recover" {
				return fail(ReconciliationRequired, "foreign_intent")
			}
			ids = append(ids, j.UnresolvedIntentID)
		}
	}
	for _, id := range ids {
		r, e := o.backend.Reconcile(ctx, i.guard, id)
		if e != nil || r.OperationID != id {
			return fail(ReconciliationRequired, "intent_readback")
		}
		s, e := o.backend.Load(ctx, i.guard)
		if e != nil || s == nil || s.Generation != i.claim.Epoch || recoveryConsistency(s) != nil {
			return fail(ReconciliationRequired, "intent_state")
		}
		c := s.Claims[i.claim.ID]
		if !claimBound(s, c) || c.Caller != i.claim.Caller {
			return fail(ReconciliationRequired, "intent_claim")
		}
		switch r.Certainty {
		case "not_committed":
			if _, exists := s.Commits[id]; exists || len(c.EffectIDs) > 0 {
				return fail(ReconciliationRequired, "intent_conflict")
			}
		case "committed":
			stored, ok := s.Commits[id]
			if !ok || !receiptMatches(r, stored) || len(c.EffectIDs) == 0 {
				return fail(ReconciliationRequired, "intent_witness")
			}
			c.Phase = "EFFECT_UNCERTAIN"
			c.NoEffectProved = false
		default:
			return fail(ReconciliationRequired, "intent_unresolved")
		}
		for _, j := range s.Journal {
			if j.UnresolvedIntentID == id && j.ClaimID == c.ID {
				j.UnresolvedIntentID = ""
			}
		}
		i.snapshot = s
		i.claim = c
		if e = o.persistClaim(ctx, i); e != nil {
			return e
		}
	}
	return nil
}
