package sessionowner

import (
	"context"
	"sort"
)

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
func validateIntentReservations(s *Snapshot, j *JournalEntry) error {
	bad := func() error { return fail(ReconciliationRequired, "intent_reservation") }
	if j == nil {
		return bad()
	}
	if len(j.Intents) > 4096 {
		return bad()
	}
	for id, r := range j.Intents {
		if id != r.ID || !validID(id) || !validID(r.EffectID) || r.BindingID != s.BindingID || r.Caller != j.Caller || r.RequestID != j.RequestID || r.RequestDigest != j.Digest || r.OperationID != j.OperationID || r.OperationVersion != j.OperationVersion || r.Epoch != j.Generation || r.ClaimID != j.ClaimID {
			return bad()
		}
		phase := map[string]string{"execute": "EXECUTING", "readiness": "WAIT_READINESS", "acquire_candidate": "ACQUIRING", "prove_candidate": "PROVING", "reconcile": "RECONCILING"}[r.Step]
		if phase == "" || r.Phase != phase || r.BeforeDispatches < 0 || r.BeforeAcquisitionAttempts < 0 || r.BeforeProofAttempts < 0 {
			return bad()
		}
		stored, committed := s.Commits[id]
		switch r.State {
		case "prepared":
			if committed || containsID(j.EffectIDs, r.EffectID) || j.Dispatches != r.BeforeDispatches {
				return bad()
			}
			if r.ClaimID != "" {
				c := s.Claims[r.ClaimID]
				if c == nil || c.AcquisitionAttempts != r.BeforeAcquisitionAttempts || c.ProofAttempts != r.BeforeProofAttempts {
					return bad()
				}
			}
		case "not_committed":
			if committed || containsID(j.EffectIDs, r.EffectID) {
				return bad()
			}
		case "committed":
			if !committed || stored.OperationID != id || stored.Certainty != "committed" || stored.Generation != r.Epoch || !containsID(j.EffectIDs, r.EffectID) || j.Dispatches <= r.BeforeDispatches {
				return bad()
			}
		default:
			return bad()
		}
	}
	return nil
}

// Every possibly attempted permit has a pre-existing durable preparation record.
// Resume never infers absence from missing counters, an error, or a failed later
// cleanup write. It queries the exact recorded transaction under the same guard.
func (o *Owner) reconcilePreparedIntents(ctx context.Context, i *Invocation) error {
	type pending struct{ key, id string }
	entries := []pending{}
	for key, j := range i.snapshot.Journal {
		for id, r := range j.Intents {
			prepared := r.State == "prepared"
			issued := r.State == "committed" && j.Certainty == "uncertain" && j.ResolutionID == "" && i.claim != nil && containsID(i.claim.EffectIDs, r.EffectID)
			if !prepared && !issued {
				continue
			}
			if j.ClaimID != i.claim.ID || j.Caller != i.claim.Caller || j.Generation != i.claim.Epoch || j.RequestKind != "recover" {
				return fail(ReconciliationRequired, "foreign_prepared_intent")
			}
			entries = append(entries, pending{key, id})
		}
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].id < entries[b].id })
	for _, p := range entries {
		r, e := o.backend.Reconcile(ctx, i.guard, p.id)
		if e != nil || r.OperationID != p.id {
			return fail(ReconciliationRequired, "prepared_intent_readback")
		}
		s, e := o.backend.Load(ctx, i.guard)
		if e != nil || s == nil || s.Generation != i.claim.Epoch || recoveryConsistency(s) != nil {
			return fail(ReconciliationRequired, "prepared_intent_state")
		}
		j := s.Journal[p.key]
		if j == nil {
			return fail(ReconciliationRequired, "prepared_intent_journal")
		}
		record, ok := j.Intents[p.id]
		c := s.Claims[i.claim.ID]
		if !ok || !claimBound(s, c) || record.ClaimID != c.ID || record.Caller != c.Caller {
			return fail(ReconciliationRequired, "prepared_intent_scope")
		}
		switch r.Certainty {
		case "not_committed":
			if record.State == "committed" || containsID(j.EffectIDs, record.EffectID) {
				return fail(ReconciliationRequired, "prepared_intent_conflict")
			}
			if _, exists := s.Commits[p.id]; exists {
				return fail(ReconciliationRequired, "prepared_intent_conflict")
			}
			record.State = "not_committed"
			j.Intents[p.id] = record
		case "committed":
			stored, exists := s.Commits[p.id]
			if !exists || !receiptMatches(r, stored) || record.State != "committed" || !containsID(j.EffectIDs, record.EffectID) || !containsID(c.EffectIDs, record.EffectID) || c.ActiveStep != record.Step {
				return fail(ReconciliationRequired, "prepared_intent_witness")
			}
			c.Phase = "EFFECT_UNCERTAIN"
			c.NoEffectProved = false
		default:
			return fail(ReconciliationRequired, "prepared_intent_unresolved")
		}
		i.installSnapshot(s)
		i.claim = c
		if e = o.persistClaim(ctx, i); e != nil {
			return e
		}
	}
	return nil
}
