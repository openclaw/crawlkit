package sessionowner

import (
	"sort"
)

// Dependency identities are derived from the authoritative ORIGINAL graph at
// preparation, independently of the proposed Transactions rows. Native backend
// receipts bind this immutable graph and the complete proposal across revisions.
type SettlementDependency struct{ ID, Role, Link, Witness string }

func deriveSettlementDependencies(s *Snapshot, key string) ([]SettlementDependency, error) {
	root := s.Journal[key]
	if root == nil || root.Composition == nil {
		return nil, fail(ReconciliationRequired, "settlement_dependency_root")
	}
	c := root.Composition
	byID := map[string]SettlementDependency{}
	add := func(id, role, link string) error {
		if !validID(id) || len(byID) >= 256 {
			return fail(ReconciliationRequired, "settlement_dependency_capacity")
		}
		if _, exists := byID[id]; exists {
			return fail(ReconciliationRequired, "settlement_dependency_duplicate")
		}
		w, e := settlementDependencyWitness(s, role, link, id)
		if e != nil {
			return e
		}
		byID[id] = SettlementDependency{id, role, link, w}
		return nil
	}
	if e := add(c.FinishID, "root_finish", key); e != nil {
		return nil, e
	}
	claims := map[string]bool{}
	for _, requestID := range []string{c.InitialID, c.RecoveryID, c.RetryID} {
		journalKey := root.Caller + "\x00" + requestID
		j := s.Journal[journalKey]
		if j == nil {
			continue
		}
		if j.CompositionKey != key || j.Caller != root.Caller || j.RequestID != requestID {
			return nil, fail(ReconciliationRequired, "settlement_dependency_journal")
		}
		for id := range j.Intents {
			if e := add(id, "intent", journalKey); e != nil {
				return nil, e
			}
		}
		if j.UnresolvedIntentID != "" {
			if _, ok := j.Intents[j.UnresolvedIntentID]; !ok {
				return nil, fail(ReconciliationRequired, "settlement_unbound_intent")
			}
		}
		if j.ResponseCommitID != "" {
			if e := add(j.ResponseCommitID, "child_result", journalKey); e != nil {
				return nil, e
			}
		}
		if j.ClaimID != "" {
			claims[j.ClaimID] = true
		}
	}
	for id := range claims {
		claim := s.Claims[id]
		if !claimBound(s, claim) || claim.BindingID != s.BindingID || claim.Caller != root.Caller || claim.Epoch != root.Generation {
			return nil, fail(ReconciliationRequired, "settlement_dependency_claim")
		}
		for _, tx := range claim.PriorCommitIDs {
			if e := add(tx, "claim_prior", id); e != nil {
				return nil, e
			}
		}
		if claim.CommitOperationID != "" {
			if e := add(claim.CommitOperationID, "claim_adoption", id); e != nil {
				return nil, e
			}
		}
	}
	for key, t := range s.CandidateTransfers {
		if t == nil || !claims[t.Scope.ClaimID] {
			continue
		}
		if e := add(t.AcceptanceCommitID, "candidate_acceptance", key); e != nil {
			return nil, e
		}
		if t.Delivery != nil {
			if e := add(t.Delivery.ID, "candidate_delivery", key); e != nil {
				return nil, e
			}
		}
		if t.Retirement != nil {
			if e := add(t.Retirement.ID, "candidate_retirement", key); e != nil {
				return nil, e
			}
		}
	}
	result := make([]SettlementDependency, 0, len(byID))
	for _, d := range byID {
		result = append(result, d)
	}
	sort.Slice(result, func(a, b int) bool { return result[a].ID < result[b].ID })
	return result, nil
}
func settlementGraphWitness(r *ReadSettlementRecord) string {
	return payloadDigest([]any{r.Version, r.PreparationID, r.ResolutionID, r.RootAnchor, r.Disposition, r.Certainty, r.Journals, r.Claims, r.Dependencies})
}
func settlementProposalWitness(r *ReadSettlementRecord) string {
	copy := *r
	copy.State = ""
	return payloadDigest(copy)
}
func validateSettlementFacts(r *ReadSettlementRecord) error {
	if len(r.Dependencies) == 0 || len(r.Dependencies) != len(r.Transactions) {
		return fail(ReconciliationRequired, "settlement_dependency_coverage")
	}
	facts := map[string]SettlementTransaction{}
	for _, f := range r.Transactions {
		if _, dup := facts[f.ID]; dup {
			return fail(ReconciliationRequired, "settlement_fact_duplicate")
		}
		facts[f.ID] = f
	}
	for _, d := range r.Dependencies {
		f, ok := facts[d.ID]
		if !ok || f.Role != d.Role || (f.Certainty != "committed" && f.Certainty != "not_committed") {
			return fail(ReconciliationRequired, "settlement_dependency_role")
		}
		if d.Role == "root_finish" && f.Certainty != "not_committed" || d.Role == "claim_prior" && f.Certainty != "not_committed" {
			return fail(ReconciliationRequired, "settlement_dependency_disposition")
		}
	}
	return nil
}

// Backend-owned receipt witnesses are not a caller-provided self-hash. For a
// preparation, derive the required graph from current authority before writing.
// Resolution may only carry the graph/proposal positively bound by that receipt.
func settlementCommitWitness(current, next *Snapshot, id string) (string, string, error) {
	for key, j := range next.Journal {
		if j == nil || j.Composition == nil || j.Composition.Settlement == nil {
			continue
		}
		r := j.Composition.Settlement
		if id != r.PreparationID && id != r.ResolutionID {
			continue
		}
		if e := validateSettlementFacts(r); e != nil {
			return "", "", e
		}
		graph, proposal := settlementGraphWitness(r), settlementProposalWitness(r)
		if id == r.PreparationID {
			if e := validateReadSettlement(next, key, j); e != nil {
				return "", "", e
			}
			required, e := deriveSettlementDependencies(current, key)
			if e != nil || !equalJSON(normalize(required), normalize(r.Dependencies)) {
				return "", "", fail(ReconciliationRequired, "settlement_original_dependency_graph")
			}
			if r.State != "prepared" {
				return "", "", fail(ReconciliationRequired, "settlement_preparation_state")
			}
		}
		if id == r.ResolutionID {
			p, ok := current.Commits[r.PreparationID]
			if !ok || p.Certainty != "committed" || p.SettlementGraph != graph || p.SettlementProposal != proposal || graph == "" || proposal == "" {
				return "", "", fail(ReconciliationRequired, "settlement_preparation_binding")
			}
		}
		return graph, proposal, nil
	}
	return "", "", nil
}
func validateHistoricalDependency(s *Snapshot, rootKey string, d SettlementDependency) error {
	root := s.Journal[rootKey]
	if root == nil || root.Composition == nil {
		return fail(ReconciliationRequired, "settlement_dependency_root")
	}
	switch d.Role {
	case "root_finish":
		if d.Link != rootKey || d.ID != root.Composition.FinishID {
			return fail(ReconciliationRequired, "settlement_dependency_root_link")
		}
	case "intent", "child_result":
		j := s.Journal[d.Link]
		if j == nil || j.CompositionKey != rootKey || j.Caller != root.Caller {
			return fail(ReconciliationRequired, "settlement_dependency_child_link")
		}
		if d.Role == "intent" {
			if _, ok := j.Intents[d.ID]; !ok {
				return fail(ReconciliationRequired, "settlement_dependency_intent_link")
			}
		} else if j.ResponseCommitID != d.ID {
			return fail(ReconciliationRequired, "settlement_dependency_result_link")
		}
	case "claim_adoption", "claim_prior":
		c := s.Claims[d.Link]
		linked := false
		for _, id := range []string{root.Composition.InitialID, root.Composition.RecoveryID, root.Composition.RetryID} {
			if j := s.Journal[root.Caller+"\x00"+id]; j != nil && j.ClaimID == d.Link {
				linked = true
			}
		}
		if !linked || !claimBound(s, c) || c.Caller != root.Caller || c.Epoch != root.Generation || (c.CommitOperationID != d.ID && !containsID(c.PriorCommitIDs, d.ID)) {
			return fail(ReconciliationRequired, "settlement_dependency_claim_link")
		}
	case "candidate_acceptance", "candidate_delivery", "candidate_retirement":
		t := s.CandidateTransfers[d.Link]
		if t == nil || t.Scope.Caller != root.Caller || t.Scope.Epoch != root.Generation {
			return fail(ReconciliationRequired, "settlement_dependency_candidate_link")
		}
		linked := false
		for _, ref := range root.Composition.Settlement.Claims {
			linked = linked || ref.ID == t.Scope.ClaimID
		}
		if !linked {
			return fail(ReconciliationRequired, "settlement_dependency_candidate_claim")
		}
		id := t.AcceptanceCommitID
		if d.Role == "candidate_delivery" {
			if t.Delivery == nil {
				return fail(ReconciliationRequired, "settlement_dependency_delivery")
			}
			id = t.Delivery.ID
		}
		if d.Role == "candidate_retirement" {
			if t.Retirement == nil {
				return fail(ReconciliationRequired, "settlement_dependency_retirement")
			}
			id = t.Retirement.ID
		}
		if id != d.ID {
			return fail(ReconciliationRequired, "settlement_dependency_candidate_id")
		}
	default:
		return fail(ReconciliationRequired, "settlement_dependency_unknown_role")
	}
	witness, e := settlementDependencyWitness(s, d.Role, d.Link, d.ID)
	if e != nil || witness != d.Witness {
		return fail(ReconciliationRequired, "settlement_dependency_witness")
	}
	return nil
}

func settlementDependencyWitness(s *Snapshot, role, link, id string) (string, error) {
	switch role {
	case "root_finish":
		j := s.Journal[link]
		if j == nil {
			return "", fail(ReconciliationRequired, "dependency_root")
		}
		return settlementJournalAnchor(j), nil
	case "intent":
		j := s.Journal[link]
		if j == nil {
			return "", fail(ReconciliationRequired, "dependency_journal")
		}
		r, ok := j.Intents[id]
		if !ok {
			return "", fail(ReconciliationRequired, "dependency_intent")
		}
		r.State = ""
		return payloadDigest(r), nil
	case "child_result":
		j := s.Journal[link]
		if j == nil {
			return "", fail(ReconciliationRequired, "dependency_result")
		}
		return payloadDigest([]any{j.RequestID, j.Digest, j.OperationID, j.OperationVersion, j.Generation, j.ResponseCommitID, j.PendingResponse, j.Response}), nil
	case "claim_adoption", "claim_prior":
		c := s.Claims[link]
		if c == nil {
			return "", fail(ReconciliationRequired, "dependency_claim")
		}
		return settlementClaimAnchor(c), nil
	case "candidate_acceptance":
		t := s.CandidateTransfers[link]
		if t == nil {
			return "", fail(ReconciliationRequired, "dependency_candidate")
		}
		return payloadDigest([]any{t.Scope, t.Mode, t.Ref, t.Bytes, t.TransportDigest, t.SemanticDigest, t.AcceptanceCommitID, t.ExpiresAt, t.EffectIDs}), nil
	case "candidate_delivery", "candidate_retirement":
		t := s.CandidateTransfers[link]
		if t == nil {
			return "", fail(ReconciliationRequired, "dependency_candidate")
		}
		tr := t.Delivery
		if role == "candidate_retirement" {
			tr = t.Retirement
		}
		if tr == nil {
			return "", fail(ReconciliationRequired, "dependency_transition")
		}
		return payloadDigest(identityCandidateTransition(tr)), nil
	}
	return "", fail(ReconciliationRequired, "dependency_role")
}
