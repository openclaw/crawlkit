package sessionowner

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
)

func (o *Owner) settlementAnchors(s *Snapshot, j *JournalEntry, req *Request) error {
	c := j.Composition
	if c.RegistryPolicyDigest == "" || c.AdapterArtifact == "" {
		return fail(ReconciliationRequired, "missing_original_settlement_anchor")
	}
	if c.RegistryPolicyDigest != payloadDigest(o.registry.policy) || req.Operation.AdapterID == nil || c.AdapterArtifact != o.registry.adapters[*req.Operation.AdapterID].ArtifactSHA256 || c.OperationDigest != payloadDigest(req.Operation) || c.RecoveryNewDigest != payloadDigest(o.registry.builtins["recover_new"]) || c.RecoveryResumeDigest != payloadDigest(o.registry.builtins["recover_resume"]) {
		return fail(PermissionDenied, "settlement_artifact_anchor")
	}
	planMatched := false
	for _, p := range req.Operation.BudgetPlans {
		planMatched = planMatched || payloadDigest(p) == payloadDigest(c.Plan)
	}
	if !planMatched || c.IdentityDigest != payloadDigest(s.Identity) {
		return fail(PermissionDenied, "settlement_identity_plan")
	}
	for _, id := range []string{c.InitialID, c.RecoveryID, c.RetryID} {
		child := s.Journal[j.Caller+"\x00"+id]
		if child == nil {
			continue
		}
		if child.CompositionKey != j.Caller+"\x00"+j.RequestID || child.Caller != j.Caller {
			return fail(PermissionDenied, "settlement_child_anchor")
		}
		if child.ClaimID != "" {
			claim := s.Claims[child.ClaimID]
			initial := s.Journal[j.Caller+"\x00"+c.InitialID]
			if !claimBound(s, claim) || claim.Caller != j.Caller || claim.BindingID != s.BindingID || claim.Epoch != j.Generation || initial == nil || initial.Diagnosis == nil || claim.Cause != initial.Diagnosis.Cause || claim.DiagnosisID == "" {
				return fail(PermissionDenied, "settlement_claim_anchor")
			}
			origin := s.Journal[claim.OriginJournalKey]
			if origin.OperationID != o.registry.builtins["recover_new"].ID || origin.OperationVersion != o.registry.builtins["recover_new"].Version {
				return fail(PermissionDenied, "settlement_claim_origin")
			}
			diagnosisFound := false
			for _, source := range s.Journal {
				d := source.Diagnosis
				if d == nil || d.ID != claim.DiagnosisID {
					continue
				}
				if source.Caller != claim.Caller || source.Generation != claim.Epoch || source.Semantics != "safe_read" || source.Certainty != "completed" || d.Cause != claim.Cause || d.Epoch != claim.Epoch {
					return fail(PermissionDenied, "settlement_claim_diagnosis")
				}
				v, e := DecodeJSON(source.Response, HardFrameLimit)
				if e != nil {
					return e
				}
				m, _ := object(v)
				f, _ := object(m["failure"])
				if text(m, "kind") != "failed" || text(m, "requestId") != source.RequestID || text(m, "acceptedGeneration") != claim.Epoch || text(f, "category") != d.Category {
					return fail(PermissionDenied, "settlement_diagnosis_outcome")
				}
				diagnosisFound = true
			}
			if !diagnosisFound {
				return fail(PermissionDenied, "settlement_claim_diagnosis_missing")
			}
		}
	}
	return nil
}
func (o *Owner) settlementTransaction(ctx context.Context, g *Guard, s *Snapshot, id, role string) (SettlementTransaction, error) {
	empty := SettlementTransaction{}
	if !validID(id) {
		return empty, fail(ReconciliationRequired, "missing_original_transaction")
	}
	if e := o.compositionContextError(ctx); e != nil {
		return empty, e
	}
	r, e := o.backend.Reconcile(ctx, g, id)
	if e != nil || r.OperationID != id || r.Certainty == "uncertain" {
		return empty, fail(ReconciliationRequired, "original_transaction_readback")
	}
	if e = o.compositionContextError(ctx); e != nil {
		return empty, e
	}
	stored, exists := s.Commits[id]
	switch r.Certainty {
	case "committed":
		if !exists || !committedOutcomeVisible(s, r) || !receiptMatches(r, stored) {
			return empty, fail(ReconciliationRequired, "original_transaction_receipt")
		}
	case "not_committed":
		if exists {
			return empty, fail(ReconciliationRequired, "original_transaction_conflict")
		}
	default:
		return empty, fail(ReconciliationRequired, "original_transaction_unknown")
	}
	return SettlementTransaction{ID: id, Role: role, Certainty: r.Certainty, Receipt: r}, nil
}
func (o *Owner) classifyReadSettlement(ctx context.Context, g *Guard, s *Snapshot, key string) (*ReadSettlementRecord, *ReadSettlementBlock, error) {
	root := s.Journal[key]
	c := root.Composition
	prep, resolution := settlementIDs(s, root)
	dependencies, dependencyErr := deriveSettlementDependencies(s, key)
	if dependencyErr != nil {
		return nil, &ReadSettlementBlock{Code: "original_dependency_graph", Needed: "exact original dependency roles and links", Route: "same_settlement_control", RouteAvailable: true}, nil
	}
	record := &ReadSettlementRecord{Dependencies: dependencies, Version: "read-settlement/1", State: "prepared", PreparationID: prep, ResolutionID: resolution, RootAnchor: settlementJournalAnchor(root), Disposition: "no_effect", Certainty: "not_dispatched"}
	blocked := func(code, id, claim, route string, available bool) (*ReadSettlementRecord, *ReadSettlementBlock, error) {
		block := &ReadSettlementBlock{Code: code, TransactionID: id, ClaimID: claim, Needed: code, Route: route, RouteAvailable: available}
		if claim != "" && route == "separately_authorized_exact_claim_recover" {
			block.RouteAvailable, block.RouteConstraint = o.settlementRecoveryRoute(s, root.Caller, claim)
		}
		return nil, block, nil
	}
	seen := map[string]bool{}
	add := func(id, role string) (SettlementTransaction, error) {
		if len(record.Transactions) >= 256 {
			return SettlementTransaction{}, fail(ReconciliationRequired, "settlement_graph_capacity")
		}
		p, e := o.settlementTransaction(ctx, g, s, id, role)
		if e == nil && !seen[id] {
			seen[id] = true
			record.Transactions = append(record.Transactions, p)
		}
		return p, e
	}
	if p, e := add(c.FinishID, "root_finish"); e != nil || p.Certainty != "not_committed" {
		return blocked("original_finish_not_proved_noncommit", c.FinishID, "", "same_settlement_control", true)
	}
	keys := []string{key}
	for _, id := range []string{c.InitialID, c.RecoveryID, c.RetryID} {
		k := root.Caller + "\x00" + id
		if s.Journal[k] != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	claimIDs := []string{}
	for _, k := range keys {
		j := s.Journal[k]
		record.Journals = append(record.Journals, SettlementJournal{Key: k, Anchor: settlementJournalAnchor(j), OriginalResolutionID: j.ResolutionID, OriginalUnresolvedID: j.UnresolvedIntentID})
		if k == key {
			continue
		}
		if j.ClaimID != "" && !containsID(claimIDs, j.ClaimID) {
			claimIDs = append(claimIDs, j.ClaimID)
		}
		for id, intent := range j.Intents {
			p, e := add(id, "intent")
			if e != nil {
				return blocked("intent_readback", id, j.ClaimID, "same_settlement_control", true)
			}
			if p.Certainty == "committed" {
				if intent.State != "committed" || !containsID(j.EffectIDs, intent.EffectID) {
					return blocked("intent_witness_conflict", id, j.ClaimID, "same_settlement_control", true)
				}
			} else if intent.State == "committed" || containsID(j.EffectIDs, intent.EffectID) {
				return blocked("noncommit_cannot_erase_permit", id, j.ClaimID, "same_settlement_control", true)
			}
		}
		if j.UnresolvedIntentID != "" && !seen[j.UnresolvedIntentID] {
			return blocked("unbound_unresolved_intent", j.UnresolvedIntentID, j.ClaimID, "qualified_original_outcome_reconciliation", false)
		}
		complete := false
		if j.ResponseCommitID != "" {
			p, e := add(j.ResponseCommitID, "child_result")
			if e != nil {
				return blocked("child_result_readback", j.ResponseCommitID, j.ClaimID, "same_settlement_control", true)
			}
			if p.Certainty == "committed" {
				if len(j.Response) == 0 || !bytes.Equal(j.PendingResponse, j.Response) || !receiptPayloadMatches(j, j.Response, p.Receipt, s.BindingID) {
					return blocked("child_result_witness", p.ID, j.ClaimID, "same_settlement_control", true)
				}
				out, e := o.compositionChildWitness(ctx, g, s, key, root, j)
				if e != nil {
					return blocked("child_outcome_graph", p.ID, j.ClaimID, "same_settlement_control", true)
				}
				complete = text(out, "certainty") == "completed" || j.Dispatches == 0 && text(out, "certainty") == "not_dispatched"
			} else if len(j.Response) > 0 {
				return blocked("installed_result_noncommit", p.ID, j.ClaimID, "same_settlement_control", true)
			}
		}
		if j.Dispatches > 0 {
			record.Certainty = "completed"
			record.Disposition = "known_complete"
			if !complete && j.RequestKind == "recover" {
				claim := s.Claims[j.ClaimID]
				if claim != nil && claim.Phase == "ACCEPTED" {
					out, e := o.compositionChildWitness(ctx, g, s, key, root, j)
					complete = e == nil && text(out, "kind") == "recovered"
				}
				if claim != nil && (claim.Phase == "CANDIDATE_READY" || claim.Phase == "PROVING") && claim.Candidate != nil && claim.CandidateDigest == payloadDigest(claim.Candidate) && claim.ProofAttempts == 0 && claim.NoEffectProved && len(claim.EffectIDs) == 0 && j.ResolutionID != "" && validID(claim.LastEvidenceID) {
					complete = true
					for _, intent := range j.Intents {
						if intent.State == "committed" && intent.Step != "acquire_candidate" {
							complete = false
						}
					}
					if complete {
						record.Disposition = "claim_preserved"
					}
				}
			}
			if !complete {
				route, available := "unsupported_safe_read_outcome_reconciliation", false
				if j.ClaimID != "" {
					_, available = o.adapters[*o.registry.operations[operationKey(root.OperationID, root.OperationVersion, "perform")].AdapterID].(RecoveryReconciler)
					route = "separately_authorized_exact_claim_recover"
				}
				return blocked("issued_permit_missing_completion", j.ResponseCommitID, j.ClaimID, route, available)
			}
		}
	}
	sort.Strings(claimIDs)
	for _, id := range claimIDs {
		claim := s.Claims[id]
		record.Claims = append(record.Claims, SettlementClaim{ID: id, Anchor: settlementClaimAnchor(claim), CandidateDigest: claim.CandidateDigest})
		if claim.Phase == "EFFECT_UNCERTAIN" || claim.Phase == "COMMIT_UNCERTAIN" || claim.Phase == "COMMITTING" {
			return blocked("claim_requires_exact_reconciliation", claim.CommitOperationID, id, "separately_authorized_exact_claim_recover", true)
		}
		for index, tx := range append(append([]string(nil), claim.PriorCommitIDs...), claim.CommitOperationID) {
			if tx == "" {
				continue
			}
			role := "claim_adoption"
			if index < len(claim.PriorCommitIDs) {
				role = "claim_prior"
			}
			p, e := add(tx, role)
			if e != nil {
				return blocked("claim_adoption_readback", tx, id, "same_settlement_control", true)
			}
			if tx == claim.CommitOperationID && claim.Phase == "ACCEPTED" && p.Certainty != "committed" {
				return blocked("accepted_claim_noncommit", tx, id, "same_settlement_control", true)
			}
		}
	}
	for _, transfer := range s.CandidateTransfers {
		if !containsID(claimIDs, transfer.Scope.ClaimID) {
			continue
		}
		for _, pair := range []struct{ id, role, state string }{{transfer.AcceptanceCommitID, "candidate_acceptance", transfer.State}, {func() string {
			if transfer.Delivery != nil {
				return transfer.Delivery.ID
			}
			return ""
		}(), "candidate_delivery", func() string {
			if transfer.Delivery != nil {
				return transfer.Delivery.State
			}
			return ""
		}()}, {func() string {
			if transfer.Retirement != nil {
				return transfer.Retirement.ID
			}
			return ""
		}(), "candidate_retirement", func() string {
			if transfer.Retirement != nil {
				return transfer.Retirement.State
			}
			return ""
		}()}} {
			if pair.id == "" {
				continue
			}
			p, e := add(pair.id, pair.role)
			if e != nil || pair.state == "prepared" || pair.state == "reserved" {
				return blocked("candidate_transition_requires_exact_reconciliation", pair.id, transfer.Scope.ClaimID, "separately_authorized_exact_claim_recover", true)
			}
			if (pair.state == "accepted" || pair.state == "issued" || pair.state == "delivered" || pair.state == "committed") && p.Certainty != "committed" {
				return blocked("candidate_transition_conflict", pair.id, transfer.Scope.ClaimID, "same_settlement_control", true)
			}
		}
	}
	sort.Slice(record.Transactions, func(a, b int) bool { return record.Transactions[a].ID < record.Transactions[b].ID })
	return record, nil, nil
}
func (o *Owner) verifySettlementRecord(ctx context.Context, g *Guard, s *Snapshot, key string, requireResolution bool) error {
	j := s.Journal[key]
	if j == nil || j.Composition == nil || j.Composition.Settlement == nil {
		return fail(ReconciliationRequired, "missing_settlement")
	}
	r := j.Composition.Settlement
	if e := validateReadSettlement(s, key, j); e != nil {
		return e
	}
	prep, e := o.settlementTransaction(ctx, g, s, r.PreparationID, "terminal_preparation")
	if e != nil || prep.Certainty != "committed" {
		return fail(ReconciliationRequired, "terminal_preparation_unconfirmed")
	}
	if prep.Receipt.SettlementGraph == "" || prep.Receipt.SettlementGraph != settlementGraphWitness(r) {
		return fail(ReconciliationRequired, "settlement_original_graph_binding")
	}
	observed := map[string]SettlementTransaction{}
	for _, dependency := range r.Dependencies {
		if e := validateHistoricalDependency(s, key, dependency); e != nil {
			return e
		}
		current, e := o.settlementTransaction(ctx, g, s, dependency.ID, dependency.Role)
		if e != nil {
			return e
		}
		observed[dependency.ID] = current
	}
	if e := validateSettlementFacts(r); e != nil {
		return e
	}
	if prep.Receipt.SettlementProposal == "" || prep.Receipt.SettlementProposal != settlementProposalWitness(r) {
		return fail(ReconciliationRequired, "settlement_original_proposal_binding")
	}
	for _, fact := range r.Transactions {
		current := observed[fact.ID]
		if current.Certainty != fact.Certainty || current.Certainty == "committed" && !receiptMatches(current.Receipt, fact.Receipt) {
			return fail(ReconciliationRequired, "settlement_dependency_changed")
		}
	}
	if requireResolution {
		resolved, e := o.settlementTransaction(ctx, g, s, r.ResolutionID, "terminal_resolution")
		if e != nil || resolved.Certainty != "committed" || r.State != "resolved" {
			return fail(ReconciliationRequired, "terminal_resolution_unconfirmed")
		}
		if s.StateRevision == resolved.Receipt.StateRevision {
			proposed, e := s.Clone()
			if e != nil {
				return e
			}
			proposed.StateRevision--
			proposed.Commits = map[string]CommitReceipt{}
			raw, _ := json.Marshal(proposed)
			if Hash(raw) != resolved.Receipt.Digest {
				return fail(ReconciliationRequired, "terminal_resolution_digest")
			}
		}
		if resolved.Receipt.SettlementGraph != prep.Receipt.SettlementGraph || resolved.Receipt.SettlementProposal != prep.Receipt.SettlementProposal {
			return fail(ReconciliationRequired, "settlement_resolution_proposal")
		}
		if resolved.Receipt.Generation != prep.Receipt.Generation || resolved.Receipt.StateRevision <= prep.Receipt.StateRevision {
			return fail(ReconciliationRequired, "terminal_resolution_scope")
		}
	}
	return o.compositionContextError(ctx)
}
func (o *Owner) continueReadSettlement(ctx context.Context, g *Guard, s *Snapshot, key string) (ReadSettlementStatus, error) {
	j := s.Journal[key]
	r := j.Composition.Settlement
	if e := o.verifySettlementRecord(ctx, g, s, key, false); e != nil {
		return o.blockSettlement(s, j, "terminal_preparation_or_dependency_readback", r.PreparationID, "", "same_settlement_control", true), nil
	}
	disposition, e := o.settlementTransaction(ctx, g, s, r.ResolutionID, "terminal_resolution")
	if e != nil {
		return o.blockSettlement(s, j, "terminal_resolution_readback", r.ResolutionID, "", "same_settlement_control", true), nil
	}
	if disposition.Certainty == "not_committed" {
		if r.State != "prepared" {
			return o.blockSettlement(s, j, "terminal_resolution_conflict", r.ResolutionID, "", "same_settlement_control", true), nil
		}
		desired, e := s.Clone()
		if e != nil {
			return ReadSettlementStatus{}, e
		}
		desired.Journal[key].Composition.Settlement.State = "resolved"
		loaded, e := o.saveWithID(ctx, g, desired, r.ResolutionID)
		if e != nil {
			return o.blockSettlement(s, j, "terminal_resolution_readback", r.ResolutionID, "", "same_settlement_control", true), nil
		}
		s = loaded
		j = s.Journal[key]
		r = j.Composition.Settlement
	}
	if e = o.verifySettlementRecord(ctx, g, s, key, true); e != nil {
		return o.blockSettlement(s, j, "terminal_resolution_confirmation", r.ResolutionID, "", "same_settlement_control", true), nil
	}
	if s.settlementVerified == nil {
		s.settlementVerified = map[string]settlementVerification{}
	}
	s.settlementVerified[key] = settlementVerification{s.StateRevision, r.ResolutionID, payloadDigest(r), payloadDigest(s)}
	result := settlementStatus(s, j, "settled")
	result.PreparationID = r.PreparationID
	result.ResolutionID = r.ResolutionID
	result.Disposition = r.Disposition
	result.Certainty = r.Certainty
	for _, claim := range r.Claims {
		result.RetainedClaims = append(result.RetainedClaims, claim.ID)
	}
	return result, nil
}
func (o *Owner) verifyReadSettlements(ctx context.Context, g *Guard, s *Snapshot) error {
	if s == nil {
		return fail(ReconciliationRequired, "settlement_state")
	}
	s.settlementVerified = map[string]settlementVerification{}
	for key, j := range s.Journal {
		if j == nil || j.Composition == nil || j.Composition.Settlement == nil {
			continue
		}
		if e := o.verifySettlementRecord(ctx, g, s, key, true); e != nil {
			return e
		}
		r := j.Composition.Settlement
		s.settlementVerified[key] = settlementVerification{s.StateRevision, r.ResolutionID, payloadDigest(r), payloadDigest(s)}
	}
	return nil
}
func (o *Owner) readSettledComposition(ctx context.Context, g *Guard, req *Request, s *Snapshot, key string) ([]byte, error) {
	j := s.Journal[key]
	r := j.Composition.Settlement
	if !settlementIsVerified(s, key) {
		if e := o.verifyReadSettlements(ctx, g, s); e != nil {
			return o.failed(req, s, nil, ReconciliationRequired, "uncertain", j.ReceiptID)
		}
	}
	if !settlementIsVerified(s, key) {
		return o.failed(req, s, nil, ReconciliationRequired, "uncertain", j.ReceiptID)
	}
	return o.failed(req, s, nil, Canceled, r.Certainty, r.ResolutionID)
}

// Method existence is not an effect permission. Surface independent fencing or a
// missing qualified reconciler instead of advertising an unusable shortcut.
func (o *Owner) settlementRecoveryRoute(s *Snapshot, caller, claimID string) (bool, string) {
	op, available := o.registry.builtins["recover_resume"]
	if !available || op.AdapterID == nil {
		return false, "recover_resume_not_registered"
	}
	c := s.Claims[claimID]
	if !claimBound(s, c) || c.Caller != caller {
		return true, "claim_scope_not_proved"
	}
	if c.Phase == "EFFECT_UNCERTAIN" {
		if _, ok := o.adapters[*op.AdapterID].(RecoveryReconciler); !ok {
			return true, "qualified_recovery_reconciler_missing"
		}
	}
	request := &Request{Value: map[string]any{"kind": "recover", "mode": "resume", "claimId": claimID, "expectedGeneration": c.Epoch}, Operation: op}
	if !o.exactResume(request, caller, s) {
		return true, "independent_claim_or_foreign_intent_fence"
	}
	return true, "fresh_normal_recovery_authorization_and_budget_required"
}
