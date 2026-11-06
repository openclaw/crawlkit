package sessionowner

import "context"

// CandidateTransition is retained private transaction evidence. Preparation is
// durable before its original ID can be attempted; it is not effect permission.
type CandidateTransition struct {
	ID            string `json:"id"`
	JournalKey    string `json:"journalKey"`
	RequestDigest string `json:"requestDigest"`
	From          string `json:"from"`
	To            string `json:"to"`
	State         string `json:"state"`
}

func newCandidateTransition(i *Invocation, from, to string) *CandidateTransition {
	return &CandidateTransition{ID: newID(), JournalKey: i.journalKey, RequestDigest: i.snapshot.Journal[i.journalKey].Digest, From: from, To: to, State: "prepared"}
}
func validateCandidateTransitions(s *Snapshot, t *CandidateTransfer) error {
	for _, tr := range []*CandidateTransition{t.Delivery, t.Retirement} {
		if tr == nil {
			continue
		}
		if tr.ID == t.AcceptanceCommitID || t.Delivery != nil && t.Retirement != nil && t.Delivery.ID == t.Retirement.ID {
			return fail(ReconciliationRequired, "candidate_transition_id_reuse")
		}
		j := s.Journal[tr.JournalKey]
		if !validID(tr.ID) || j == nil || j.ClaimID != t.Scope.ClaimID || j.Caller != t.Scope.Caller || j.Generation != t.Scope.Epoch || j.Digest != tr.RequestDigest || t.Scope.Direction != "authority_to_adapter" {
			return fail(ReconciliationRequired, "candidate_transition_scope")
		}
		if tr.State != "prepared" && tr.State != "committed" && tr.State != "not_committed" {
			return fail(ReconciliationRequired, "candidate_transition_state")
		}
	}
	if tr := t.Delivery; tr != nil {
		if tr.From != "issued" || tr.To != "delivered" || t.State == "reserved" || tr.State == "committed" && (t.State != "delivered" && t.State != "retired") || tr.State != "committed" && (t.State != "issued" && t.State != "retired") {
			return fail(ReconciliationRequired, "candidate_delivery_state")
		}
	}
	if tr := t.Retirement; tr != nil {
		if tr.From == "delivered" && (t.Delivery == nil || t.Delivery.State != "committed") || tr.From == "reserved" && t.Delivery != nil {
			return fail(ReconciliationRequired, "candidate_retirement_source")
		}
		if tr.To != "retired" || (tr.From != "reserved" && tr.From != "issued" && tr.From != "delivered") || tr.State == "not_committed" || tr.State == "committed" && t.State != "retired" || tr.State == "prepared" && t.State != tr.From {
			return fail(ReconciliationRequired, "candidate_retirement_state")
		}
	}
	if t.State == "delivered" && (t.Delivery == nil || t.Delivery.State != "committed") || t.State == "retired" && (t.Retirement == nil || t.Retirement.State != "committed") {
		return fail(ReconciliationRequired, "candidate_transition_missing")
	}
	return nil
}
func sameCandidateTransferIdentity(a, b *CandidateTransfer) bool {
	if a == nil || b == nil {
		return false
	}
	x, y := *a, *b
	x.State = ""
	y.State = ""
	x.Delivery = identityCandidateTransition(a.Delivery)
	x.Retirement = identityCandidateTransition(a.Retirement)
	y.Delivery = identityCandidateTransition(b.Delivery)
	y.Retirement = identityCandidateTransition(b.Retirement)
	return equalJSON(normalize(x), normalize(y))
}

func identityCandidateTransition(tr *CandidateTransition) *CandidateTransition {
	if tr == nil {
		return nil
	}
	copy := *tr
	copy.State = ""
	return &copy
}

// Always reconcile the exact original ID, including a stored committed marker:
// an issuance receipt says nothing about a later delivery or retirement commit.
func (o *Owner) readCandidateTransition(ctx context.Context, i *Invocation, key, kind string) (string, error) {
	old := i.snapshot.CandidateTransfers[key]
	if old == nil {
		return "", fail(ReconciliationRequired, "candidate_transition_missing")
	}
	id := old.AcceptanceCommitID
	var previous *CandidateTransition
	if kind == "delivery" {
		previous = old.Delivery
	} else if kind == "retirement" {
		previous = old.Retirement
	} else if kind != "issuance" {
		return "", fail(ProtocolMismatch, "candidate_transition_kind")
	}
	if kind != "issuance" {
		if previous == nil {
			return "", fail(ReconciliationRequired, "candidate_transition_missing")
		}
		id = previous.ID
	}
	receipt, e := o.backend.Reconcile(ctx, i.guard, id)
	if e != nil || ctx.Err() != nil || receipt.OperationID != id {
		return "", fail(ReconciliationRequired, "candidate_transition_readback")
	}
	loaded, e := o.backend.Load(ctx, i.guard)
	if e != nil || ctx.Err() != nil {
		return "", fail(ReconciliationRequired, "candidate_transition_load")
	}
	record := loaded.CandidateTransfers[key]
	if !sameCandidateTransferIdentity(old, record) || validateCandidateTransitions(loaded, record) != nil {
		return "", fail(ReconciliationRequired, "candidate_transition_identity")
	}
	var tr *CandidateTransition
	if kind == "delivery" {
		tr = record.Delivery
	} else if kind == "retirement" {
		tr = record.Retirement
	}
	if previous != nil && (tr == nil || tr.ID != previous.ID || tr.JournalKey != previous.JournalKey || tr.RequestDigest != previous.RequestDigest || tr.From != previous.From || tr.To != previous.To) {
		return "", fail(ReconciliationRequired, "candidate_transition_changed")
	}
	stored, hasReceipt := loaded.Commits[id]
	switch receipt.Certainty {
	case "committed":
		if !hasReceipt || !receiptMatches(receipt, stored) {
			return "", fail(ReconciliationRequired, "candidate_transition_receipt")
		}
		if kind == "issuance" && record.State == "reserved" || kind != "issuance" && tr.State != "committed" {
			return "", fail(ReconciliationRequired, "candidate_transition_commit")
		}
	case "not_committed":
		if hasReceipt {
			return "", fail(ReconciliationRequired, "candidate_transition_contradiction")
		}
		if kind == "issuance" {
			if record.State != "reserved" && (record.State != "retired" || record.Retirement.From != "reserved") {
				return "", fail(ReconciliationRequired, "candidate_issuance_noncommit")
			}
		} else if tr.State == "committed" {
			return "", fail(ReconciliationRequired, "candidate_transition_noncommit")
		}
	default:
		return "", fail(ReconciliationRequired, "candidate_transition_uncertain")
	}
	i.installSnapshot(loaded)
	return receipt.Certainty, nil
}
func (o *Owner) retireOutboundTransfers(ctx context.Context, i *Invocation) error {
	for key, original := range i.snapshot.CandidateTransfers {
		if original.Scope.ClaimID != i.claim.ID || original.Scope.Direction != "authority_to_adapter" {
			continue
		}
		if _, e := o.readCandidateTransition(ctx, i, key, "issuance"); e != nil {
			return e
		}
		deliveryNotCommitted := false
		if i.snapshot.CandidateTransfers[key].Delivery != nil {
			certainty, e := o.readCandidateTransition(ctx, i, key, "delivery")
			if e != nil {
				return e
			}
			deliveryNotCommitted = certainty == "not_committed"
		}
		t := i.snapshot.CandidateTransfers[key]
		if t.Retirement != nil {
			certainty, e := o.readCandidateTransition(ctx, i, key, "retirement")
			if e != nil {
				return e
			}
			if certainty == "committed" {
				continue
			}
		}
		if i.snapshot.CandidateTransfers[key].Retirement == nil {
			desired, e := i.snapshot.Clone()
			if e != nil {
				return e
			}
			t = desired.CandidateTransfers[key]
			if deliveryNotCommitted {
				t.Delivery.State = "not_committed"
			}
			t.Retirement = newCandidateTransition(i, t.State, "retired")
			loaded, e := o.save(ctx, i.guard, desired)
			if e != nil {
				return e
			}
			i.installSnapshot(loaded)
		}
		desired, e := i.snapshot.Clone()
		if e != nil {
			return e
		}
		t = desired.CandidateTransfers[key]
		t.State = "retired"
		t.Retirement.State = "committed"
		loaded, e := o.saveWithID(ctx, i.guard, desired, t.Retirement.ID)
		if e != nil {
			return e
		}
		i.installSnapshot(loaded)
	}
	return nil
}

// Only the private handoff installs this bound. Existing intent/cleanup contexts
// retain their reviewed semantics. A secret handoff never detaches cancellation.
type candidateStorageBoundKey struct{}
type candidateStorageBound struct{ check func() error }

func checkCandidateStorageBound(ctx context.Context) error {
	if b, ok := ctx.Value(candidateStorageBoundKey{}).(candidateStorageBound); ok {
		return b.check()
	}
	return nil
}
