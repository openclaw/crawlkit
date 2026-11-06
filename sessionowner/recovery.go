package sessionowner

import (
	"context"
	"encoding/json"
	"strconv"
	"time"
)

func (o *Owner) claimTransition(ctx context.Context, i *Invocation, phase string, e ClaimEvidence) error {
	c, err := AdvanceClaim(*i.claim, phase, e, i.plan, o.now())
	if err != nil {
		return err
	}
	i.snapshot.ClaimVersion, err = nextRevision(i.snapshot.ClaimVersion)
	if err != nil {
		return err
	}
	i.snapshot.Claims[c.ID] = &c
	i.snapshot, err = o.save(ctx, i.guard, i.snapshot)
	if err == nil {
		i.claim = i.snapshot.Claims[c.ID]
	}
	return err
}
func (o *Owner) persistClaim(ctx context.Context, i *Invocation) error {
	var e error
	i.snapshot.Claims[i.claim.ID] = i.claim
	i.snapshot.ClaimVersion, e = nextRevision(i.snapshot.ClaimVersion)
	if e != nil {
		return e
	}
	i.snapshot, e = o.save(ctx, i.guard, i.snapshot)
	if e == nil {
		i.claim = i.snapshot.Claims[i.claim.ID]
	}
	return e
}
func (o *Owner) knownRecoveryStep(ctx context.Context, i *Invocation) error {
	evidence := newID()
	resolveClaimJournals(i.snapshot, i.claim, evidence)
	i.claim.LastEvidenceID = evidence
	i.claim.ActiveStep = ""
	i.claim.ActiveJournalKey = ""
	i.claim.EffectIDs = nil
	i.claim.NoEffectProved = true
	return o.persistClaim(ctx, i)
}
func (o *Owner) rejectCandidate(ctx context.Context, i *Invocation, code Code) ([]byte, error) {
	i.claim.Phase = "REJECTED"
	resolveClaimJournals(i.snapshot, i.claim, newID())
	i.claim.NoEffectProved = len(i.claim.EffectIDs) == 0
	if e := o.persistClaim(context.WithoutCancel(ctx), i); e != nil {
		return o.failed(i.request, nil, nil, StorageFailure, "uncertain", i.active.receipt)
	}
	return o.finishFailure(ctx, i, code, recoveryCertainty(i), i.claim)
}
func (o *Owner) pauseRecoveryStep(ctx context.Context, i *Invocation, err error) ([]byte, error) {
	if i.intentUncertain {
		// The attempted transaction has no authoritative disposition. Do not save
		// either its proposed witness/counter state or an assumed no-effect state.
		return o.failed(i.request, nil, nil, ReconciliationRequired, "uncertain", i.active.receipt)
	}
	if len(i.claim.EffectIDs) > 0 {
		i.claim.Phase = "EFFECT_UNCERTAIN"
		i.claim.NoEffectProved = false
	} else {
		i.claim.NoEffectProved = true
		switch i.step {
		case "prove_candidate":
			i.claim.Phase = "CANDIDATE_READY"
		default:
			i.claim.Phase = "WAIT_READINESS"
			i.claim.RetryNotBefore = i.snapshot.Fences["recovery"]
		}
		resolveClaimJournals(i.snapshot, i.claim, newID())
		i.claim.ActiveStep = ""
		i.claim.ActiveJournalKey = ""
	}
	if e := o.persistClaim(context.WithoutCancel(ctx), i); e != nil {
		return o.failed(i.request, nil, nil, StorageFailure, "uncertain", i.active.receipt)
	}
	code := ErrorCode(err)
	if ctx.Err() != nil {
		code = Canceled
	}
	if i.claim.Phase == "EFFECT_UNCERTAIN" {
		code = ReconciliationRequired
	}
	return o.finishFailure(ctx, i, code, recoveryCertainty(i), i.claim)
}
func (o *Owner) recover(ctx context.Context, i *Invocation) ([]byte, error) {
	req := i.request
	j := i.snapshot.Journal[i.journalKey]
	caller := j.Caller
	var err error
	if text(req.Value, "mode") == "new" {
		diagnosis, e := o.validateRejected(req, caller, i.snapshot)
		if e != nil {
			return o.finishFailure(ctx, i, PermissionDenied, "not_dispatched", nil)
		}
		if t := i.snapshot.Fences["recovery"]; t.After(o.now()) {
			return o.finishFailure(ctx, i, Code("throttled"), "not_dispatched", nil)
		}
		var fresh bool
		i.claim, fresh, err = ClaimForGeneration(i.snapshot, text(req.Value, "cause"), o.now(), time.Duration(req.Operation.MinimumRetryDelayMs)*time.Millisecond)
		if err != nil {
			return o.finishFailure(ctx, i, ErrorCode(err), "not_dispatched", nil)
		}
		if !fresh {
			return o.finishFailure(ctx, i, ReconciliationRequired, "not_dispatched", i.claim)
		}
		i.claim.BindingID = i.snapshot.BindingID
		i.claim.Caller = caller
		i.claim.OriginRequestID = j.RequestID
		i.claim.OriginJournalKey = i.journalKey
		i.claim.DiagnosisID = diagnosis
		j.ClaimID = i.claim.ID
		if err = o.persistClaim(ctx, i); err != nil {
			return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
		}
	} else {
		i.claim = i.snapshot.Claims[text(req.Value, "claimId")]
		if !claimBound(i.snapshot, i.claim) || i.claim.Caller != caller || i.claim.Epoch != text(req.Value, "expectedGeneration") {
			return o.finishFailure(ctx, i, PermissionDenied, "not_dispatched", nil)
		}
		j.ClaimID = i.claim.ID
		if err = o.persistClaim(ctx, i); err != nil {
			return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
		}
		if err = o.reconcileUnresolvedIntents(ctx, i); err != nil {
			return o.finishFailure(ctx, i, ReconciliationRequired, "uncertain", i.claim)
		}
		if i.claim.Phase == "COMMITTING" || i.claim.Phase == "COMMIT_UNCERTAIN" {
			return o.reconcileRecoveryCommit(ctx, i)
		}
		if err = o.restoreCandidateTransfer(ctx, i); err != nil {
			return o.finishFailure(ctx, i, ErrorCode(err), "uncertain", i.claim)
		}
		if !o.recoveryEligible(i) {
			return o.finishFailure(ctx, i, Code("throttled"), "not_dispatched", i.claim)
		}
		if i.claim.Phase == "EFFECT_UNCERTAIN" {
			if err = o.reconcileRecoveryEffect(ctx, i); err != nil {
				return o.finishFailure(ctx, i, ErrorCode(err), "uncertain", i.claim)
			}
		}
		switch i.claim.Phase {
		case "WAIT_READINESS", "CANCELLED_BEFORE_EFFECT":
			if err = o.claimTransition(ctx, i, "RESERVED", ClaimEvidence{ID: newID(), NoEffect: true}); err != nil {
				return o.finishFailure(ctx, i, ErrorCode(err), "not_dispatched", i.claim)
			}
		case "ACQUIRING", "PROVING":
			// Persisted phase reservation with no permit is positive local no-effect
			// evidence. A permit/counter is never refunded after possible effects.
			if len(i.claim.EffectIDs) != 0 {
				return o.finishFailure(ctx, i, ReconciliationRequired, "uncertain", i.claim)
			}
			if i.claim.Phase == "ACQUIRING" {
				i.claim.Phase = "WAIT_READINESS"
				i.claim.NoEffectProved = true
				if err = o.persistClaim(ctx, i); err != nil {
					return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
				}
				if err = o.claimTransition(ctx, i, "RESERVED", ClaimEvidence{ID: newID(), NoEffect: true}); err != nil {
					return o.finishFailure(ctx, i, ErrorCode(err), "not_dispatched", i.claim)
				}
				break
			}
			i.claim.Phase = "CANDIDATE_READY"
			i.claim.NoEffectProved = true
		case "REJECTED":
			return o.finishFailure(ctx, i, Code("rejected_generation"), "completed", i.claim)
		case "CANDIDATE_READY", "PROVED", "RESERVED":
		default:
			return o.finishFailure(ctx, i, ReconciliationRequired, "not_dispatched", i.claim)
		}
	}
	return o.continueRecovery(ctx, i)
}
func (o *Owner) continueRecovery(ctx context.Context, i *Invocation) ([]byte, error) {
	req := i.request
	binding := o.registry.adapters[*req.Operation.AdapterID]
	if i.claim.Candidate != nil && (!i.claim.CandidateExpires.After(o.now()) || i.claim.CandidateDigest != payloadDigest(i.claim.Candidate)) {
		return o.rejectCandidate(ctx, i, Code("rejected_generation"))
	}
	if i.claim.Phase == "RESERVED" && !o.warm() && i.plan.Readiness != "none" {
		if e := o.bindStep(ctx, i, "readiness"); e != nil {
			return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
		}
		if _, e := o.execute(ctx, i); e != nil {
			return o.pauseRecoveryStep(ctx, i, e)
		}
		if e := o.knownRecoveryStep(ctx, i); e != nil {
			return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
		}
	}
	if i.claim.Phase == "RESERVED" {
		if e := o.claimTransition(ctx, i, "ACQUIRING", ClaimEvidence{ID: newID()}); e != nil {
			return o.finishFailure(ctx, i, ErrorCode(e), "not_dispatched", i.claim)
		}
		if e := o.bindStep(ctx, i, "acquire_candidate"); e != nil {
			return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
		}
		output, e := o.execute(ctx, i)
		if e != nil {
			return o.pauseRecoveryStep(ctx, i, e)
		}
		if len(i.claim.EffectIDs) == 0 {
			return o.pauseRecoveryStep(ctx, i, fail(ProtocolMismatch, "acquisition_without_permit"))
		}
		if e = o.checkCandidateAdmission(i, output.Candidate, output.CandidateExpires); e != nil {
			return o.rejectCandidate(ctx, i, ErrorCode(e))
		}
		if output.CandidateTransferID != "" {
			transfer := i.snapshot.CandidateTransfers[output.CandidateTransferID]
			if transfer == nil || transfer.State != "accepted" || transfer.Scope.ClaimID != i.claim.ID || transfer.SemanticDigest != payloadDigest(output.Candidate) || !transfer.ExpiresAt.Equal(output.CandidateExpires) {
				return o.finishFailure(ctx, i, ReconciliationRequired, "uncertain", i.claim)
			}
			i.claim.CandidateTransferKey = output.CandidateTransferID
		}
		if e = o.claimTransition(ctx, i, "CANDIDATE_READY", ClaimEvidence{ID: newID(), Candidate: output.Candidate, CandidateExpires: output.CandidateExpires}); e != nil {
			return o.finishFailure(ctx, i, ErrorCode(e), "uncertain", i.claim)
		}
		if e = o.knownRecoveryStep(ctx, i); e != nil {
			return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
		}
	}
	if i.claim.Phase == "CANDIDATE_READY" {
		if e := o.claimTransition(ctx, i, "PROVING", ClaimEvidence{ID: newID()}); e != nil {
			return o.finishFailure(ctx, i, ErrorCode(e), "not_dispatched", i.claim)
		}
		if e := o.bindStep(ctx, i, "prove_candidate"); e != nil {
			return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
		}
		i.Candidate = cloneMap(i.claim.Candidate)
		output, e := o.execute(ctx, i)
		if e != nil {
			return o.pauseRecoveryStep(ctx, i, e)
		}
		if len(i.claim.EffectIDs) == 0 {
			return o.pauseRecoveryStep(ctx, i, fail(ProtocolMismatch, "proof_without_permit"))
		}
		if binding.ProofSchemaID == nil || o.proof == nil || o.registry.schemas.Validate(*binding.ProofSchemaID, normalize(output.Proof)) != nil || o.proof(ctx, i.snapshot, i.claim.Candidate, output.Proof) != nil {
			return o.rejectCandidate(ctx, i, Code("wrong_account"))
		}
		if e = o.claimTransition(ctx, i, "PROVED", ClaimEvidence{ID: newID(), Proof: output.Proof}); e != nil {
			return o.finishFailure(ctx, i, ErrorCode(e), "uncertain", i.claim)
		}
		if e = o.knownRecoveryStep(ctx, i); e != nil {
			return o.failed(req, nil, nil, StorageFailure, "uncertain", i.active.receipt)
		}
	}
	if i.claim.Phase != "PROVED" || ctx.Err() != nil {
		return o.finishFailure(ctx, i, ReconciliationRequired, recoveryCertainty(i), i.claim)
	}
	return o.publishRecovery(ctx, i)
}
func (o *Owner) publishRecovery(ctx context.Context, i *Invocation) ([]byte, error) {
	if !i.claim.CandidateExpires.After(o.now()) || i.claim.ProofCandidateDigest != i.claim.CandidateDigest {
		return o.rejectCandidate(ctx, i, Code("rejected_generation"))
	}
	if i.claim.CommitAttempts >= 3 {
		return o.finishFailure(ctx, i, ReconciliationRequired, "not_dispatched", i.claim)
	}
	if i.claim.CommitOperationID != "" {
		i.claim.PriorCommitIDs = append(i.claim.PriorCommitIDs, i.claim.CommitOperationID)
	}
	i.claim.CommitAttempts++
	i.claim.CommitRequestID = text(i.request.Value, "requestId")
	i.claim.CommitOperationID = newID()
	if i.claim.ProposedGeneration == "" {
		i.claim.ProposedGeneration = newID()
	}
	i.claim.CommitBaseRevision = i.snapshot.StateRevision + 1
	if e := o.claimTransition(ctx, i, "COMMITTING", ClaimEvidence{ID: newID()}); e != nil {
		return o.failed(i.request, nil, nil, StorageFailure, "uncertain", i.active.receipt)
	}
	before, err := i.snapshot.Clone()
	if err != nil {
		return nil, err
	}
	i.snapshot.Credentials = cloneMap(i.claim.Candidate)
	i.snapshot.Identity = cloneMap(i.claim.Proof)
	i.snapshot.Provenance = map[string]any{"claimId": i.claim.ID, "adapterArtifact": o.registry.adapters[*i.request.Operation.AdapterID].ArtifactSHA256}
	i.snapshot.Generation = i.claim.ProposedGeneration
	i.snapshot.AuthBlocked = false
	i.snapshot.CredentialRevision, err = nextRevision(i.snapshot.CredentialRevision)
	if err != nil {
		return nil, err
	}
	i.claim.Phase = "ACCEPTED"
	i.snapshot.Claims[i.claim.ID] = i.claim
	resolveClaimJournals(i.snapshot, i.claim, newID())
	i.snapshot.ClaimVersion, err = nextRevision(i.snapshot.ClaimVersion)
	if err != nil {
		return nil, err
	}
	v := o.baseOutcome(i.request, i.snapshot, "completed", i.active.receipt)
	v["kind"] = "recovered"
	v["claim"] = claimStatus(i.claim)
	v["stateRevision"] = strconv.FormatUint(i.snapshot.StateRevision+1, 10)
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if err = o.registry.ValidateResponse(raw, i.request); err != nil {
		return nil, err
	}
	i.claim.AcceptedOutcome = append([]byte(nil), raw...)
	j := i.snapshot.Journal[i.journalKey]
	j.Response = raw
	j.Certainty = "completed"
	j.Phase = "EXECUTE_RESULT"
	commitID := i.claim.CommitOperationID
	i.snapshot, err = o.saveWithID(ctx, i.guard, i.snapshot, commitID)
	if err != nil {
		// Never publish the mutated desired snapshot after an uncertain commit.
		i.snapshot = before
		i.claim = before.Claims[i.claim.ID]
		cleanup, stop, e := o.storageContext(ctx, true)
		if e == nil {
			defer stop()
			loaded, le := o.backend.Load(cleanup, i.guard)
			if le == nil && loaded.Generation == before.Generation && loaded.Claims[i.claim.ID] != nil {
				i.snapshot = loaded
				i.claim = loaded.Claims[i.claim.ID]
				i.claim.Phase = "COMMIT_UNCERTAIN"
				_ = o.persistClaim(cleanup, i)
			}
		}
		return o.failed(i.request, nil, i.claim, StorageFailure, "uncertain", i.active.receipt)
	}
	i.claim = i.snapshot.Claims[i.claim.ID]
	i.active.mu.Lock()
	i.active.certainty = "completed"
	i.active.response = append([]byte(nil), raw...)
	i.active.mu.Unlock()
	return raw, nil
}
func (o *Owner) reconcileRecoveryCommit(ctx context.Context, i *Invocation) ([]byte, error) {
	c := i.claim
	r, e := o.backend.Reconcile(ctx, i.guard, c.CommitOperationID)
	if e != nil || r.OperationID != c.CommitOperationID {
		return o.finishFailure(ctx, i, ReconciliationRequired, "uncertain", c)
	}
	loaded, e := o.backend.Load(ctx, i.guard)
	if e != nil {
		return o.failed(i.request, nil, c, StorageFailure, "uncertain", i.active.receipt)
	}
	if r.Certainty == "committed" {
		accepted := loaded.Claims[c.ID]
		if accepted == nil || accepted.Phase != "ACCEPTED" || accepted.CommitOperationID != c.CommitOperationID || r.Generation != c.ProposedGeneration {
			return o.failed(i.request, nil, c, ReconciliationRequired, "uncertain", i.active.receipt)
		}
		return o.acceptedReadback(ctx, i.guard, i.request, c.Caller, loaded, accepted)
	}
	if r.Certainty != "not_committed" || loaded.Generation != c.Epoch || loaded.Claims[c.ID] == nil {
		return o.finishFailure(ctx, i, ReconciliationRequired, "uncertain", c)
	}
	i.snapshot = loaded
	i.claim = loaded.Claims[c.ID]
	if !o.recoveryEligible(i) {
		return o.finishFailure(ctx, i, Code("throttled"), "not_dispatched", i.claim)
	}
	if !i.claim.CandidateExpires.After(o.now()) || i.claim.CandidateDigest != payloadDigest(i.claim.Candidate) || i.claim.ProofDigest != payloadDigest(i.claim.Proof) || i.claim.ProofCandidateDigest != i.claim.CandidateDigest {
		return o.rejectCandidate(ctx, i, Code("rejected_generation"))
	}
	// New explicit resume, proved not-committed, retained valid proof: a NEW commit
	// identity may be published. The uncertain commit is never blindly repeated.
	i.claim.Phase = "PROVED"
	resolveClaimJournals(i.snapshot, i.claim, newID())
	if e = o.persistClaim(ctx, i); e != nil {
		return o.failed(i.request, nil, nil, StorageFailure, "uncertain", i.active.receipt)
	}
	return o.publishRecovery(ctx, i)
}
func (o *Owner) reconcileRecoveryEffect(ctx context.Context, i *Invocation) error {
	c := i.claim
	binding := o.registry.adapters[*i.request.Operation.AdapterID]
	a := o.adapters[binding.ID]
	reconciler, ok := a.(RecoveryReconciler)
	if !ok || a.ArtifactID() != binding.ArtifactSHA256 {
		return fail(ReconciliationRequired, "adapter_reconciliation")
	}
	old := i.snapshot.Journal[c.ActiveJournalKey]
	if old == nil || len(c.EffectIDs) == 0 {
		return fail(ReconciliationRequired, "effect_binding")
	}
	input := RecoveryReconcileRequest{BindingID: c.BindingID, ClaimID: c.ID, Caller: c.Caller, Epoch: c.Epoch, RequestID: old.RequestID, Step: c.ActiveStep, CandidateDigest: c.CandidateDigest, EffectIDs: append([]string(nil), c.EffectIDs...)}
	want := payloadDigest(input)
	i.step = "reconcile"
	i.Candidate = cloneMap(c.Candidate)
	var result RecoveryReconcileResult
	_, e := o.runPhase(ctx, i, func(phaseCtx context.Context, _ *Invocation) (AdapterOutput, error) {
		var err error
		result, err = reconciler.ReconcileRecovery(phaseCtx, i, input)
		return AdapterOutput{}, err
	})
	if e != nil {
		return fail(ReconciliationRequired, "reconciliation_failed")
	}
	if payloadDigest(result.Request) != want || !validID(result.EvidenceID) {
		return fail(ReconciliationRequired, "reconciliation_mismatch")
	}
	// A durable checkpoint refreshes the invocation from authoritative storage.
	// Use that current claim, not an alias captured before the callback.
	c = i.claim
	switch result.Status {
	case "candidate_ready":
		if input.Step != "acquire_candidate" {
			return fail(ReconciliationRequired, "candidate_reconciliation_step")
		}
		if e = o.checkCandidateAdmission(i, result.Candidate, result.CandidateExpires); e != nil {
			return e
		}
		if result.CandidateTransferID != "" {
			tr := i.snapshot.CandidateTransfers[result.CandidateTransferID]
			if tr == nil || tr.State != "accepted" || tr.Scope.ClaimID != c.ID || tr.Scope.Step != "reconcile" || tr.SemanticDigest != payloadDigest(result.Candidate) || !tr.ExpiresAt.Equal(result.CandidateExpires) {
				return fail(ReconciliationRequired, "reconciliation_transfer")
			}
			c.CandidateTransferKey = result.CandidateTransferID
		}
		c.Candidate = cloneMap(result.Candidate)
		c.CandidateDigest = payloadDigest(c.Candidate)
		c.CandidateExpires = result.CandidateExpires
		c.Phase = "CANDIDATE_READY"
	case "proved":
		if input.Step != "prove_candidate" || c.Candidate == nil || !c.CandidateExpires.After(o.now()) || binding.ProofSchemaID == nil || o.proof == nil || o.registry.schemas.Validate(*binding.ProofSchemaID, normalize(result.Proof)) != nil || o.proof(ctx, i.snapshot, c.Candidate, result.Proof) != nil {
			return fail(ReconciliationRequired, "proof_reconciliation")
		}
		c.Proof = cloneMap(result.Proof)
		c.ProofDigest = payloadDigest(c.Proof)
		c.ProofCandidateDigest = c.CandidateDigest
		c.Phase = "PROVED"
	case "no_effect":
		if input.Step == "readiness" && c.AcquisitionAttempts == 0 {
			c.Phase = "WAIT_READINESS"
			c.NoEffectProved = true
		} else {
			c.Phase = "REJECTED"
		} // consumed acquisition/proof budget is never refunded.
	case "rejected":
		c.Phase = "REJECTED"
	default:
		return fail(ReconciliationRequired, "unresolved_effect")
	}
	resolveClaimJournals(i.snapshot, c, result.EvidenceID)
	c.LastEvidenceID = result.EvidenceID
	c.EffectIDs = nil
	c.ActiveStep = ""
	c.ActiveJournalKey = ""
	return o.persistClaim(ctx, i)
}

// SafeReadRetry is a bounded owner composition helper. It never retries mutations
// or restores an expired deadline. The recover callback must use the same durable
// authority claim; it is not a login callback or a consumer-selected fallback.
func SafeReadRetry(ctx context.Context, op Operation, p BudgetPlan, deadline time.Time, now func() time.Time, read func(context.Context) error, recoverClaim func(context.Context) error) error {
	if op.Semantics != "safe_read" || p.MaxSafeReadRetries != 1 {
		return read(ctx)
	}
	err := read(ctx)
	if err == nil {
		return nil
	}
	code := ErrorCode(err)
	if code != "rejected_generation" && code != "needs_login" {
		return err
	}
	if ctx.Err() != nil || !deadline.After(now()) {
		return fail(Code("deadline_exceeded"), "retry")
	}
	if err = recoverClaim(ctx); err != nil {
		return err
	}
	if ctx.Err() != nil || !deadline.After(now()) {
		return fail(Code("deadline_exceeded"), "retry")
	}
	return read(ctx)
}
