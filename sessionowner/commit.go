package sessionowner

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// intentCommitError is private transaction evidence, not a public failure detail.
// Only exact not-committed reconciliation plus unchanged authoritative readback
// permits restoration. Other attempted transactions remain uncertain.
type intentCommitError struct {
	cause         error
	operationID   string
	notCommitted  bool
	authoritative *Snapshot
}

func (e *intentCommitError) Error() string { return string(StorageFailure) }
func (e *intentCommitError) Unwrap() error { return e.cause }
func sameSnapshot(a, b *Snapshot) bool {
	if a == nil || b == nil {
		return false
	}
	x, e := json.Marshal(a)
	if e != nil {
		return false
	}
	y, e := json.Marshal(b)
	return e == nil && bytes.Equal(x, y)
}

type operationBoundKey struct{}
type operationBound struct {
	Deadline time.Time
	Cleanup  time.Duration
}

// storageContext never creates an unbounded retry or extends the original
// operation. Detached cancellation cleanup uses only its reserved existing time.
func (o *Owner) storageContext(ctx context.Context, reconciliation bool) (context.Context, context.CancelFunc, error) {
	if b, ok := ctx.Value(candidateStorageBoundKey{}).(candidateStorageBound); ok {
		if e := b.check(); e != nil {
			return nil, nil, e
		}
		deadline, has := ctx.Deadline()
		if !has || !deadline.After(o.now()) {
			return nil, nil, fail(Canceled, "candidate_storage_deadline")
		}
		bounded, cancel := o.contextUntil(ctx, deadline)
		return bounded, cancel, nil
	}
	deadline, has := ctx.Deadline()
	bound, boundOK := ctx.Value(operationBoundKey{}).(operationBound)
	if reconciliation || !has || ctx.Err() != nil {
		if !boundOK || bound.Cleanup <= 0 {
			return nil, nil, fail(StorageFailure, "commit_budget")
		}
		limit := o.now().Add(bound.Cleanup)
		if bound.Deadline.Before(limit) {
			limit = bound.Deadline
		}
		if has && deadline.Before(limit) && ctx.Err() == nil {
			limit = deadline
		}
		deadline = limit
		ctx = context.WithoutCancel(ctx)
	}
	if !deadline.After(o.now()) {
		return nil, nil, fail(StorageFailure, "commit_deadline")
	}
	ctx, cancel := o.contextUntil(ctx, deadline)
	return ctx, cancel, nil
}

func receiptMatches(got, expected CommitReceipt) bool {
	return got.Certainty == "committed" && got.OperationID == expected.OperationID &&
		got.Generation == expected.Generation && got.StateRevision == expected.StateRevision && got.Digest == expected.Digest && got.SettlementGraph == expected.SettlementGraph && got.SettlementProposal == expected.SettlementProposal
}
func matchingReadback(loaded, desired *Snapshot, receipt CommitReceipt) bool {
	if loaded == nil || loaded.StateRevision != receipt.StateRevision || loaded.Generation != receipt.Generation {
		return false
	}
	stored, ok := loaded.Commits[receipt.OperationID]
	if !ok || !receiptMatches(stored, receipt) {
		return false
	}
	x, err := loaded.Clone()
	if err != nil {
		return false
	}
	x.StateRevision = desired.StateRevision
	x.Commits = map[string]CommitReceipt{}
	raw, err := json.Marshal(x)
	return err == nil && Hash(raw) == receipt.Digest
}

// A nil Go error is not commit evidence. Every path requires the exact positive
// receipt and matching logical snapshot readback before publishing an effect ACK.
func (o *Owner) save(ctx context.Context, g *Guard, s *Snapshot) (*Snapshot, error) {
	return o.saveWithID(ctx, g, s, newID())
}
func (o *Owner) saveWithID(ctx context.Context, g *Guard, s *Snapshot, id string) (*Snapshot, error) {
	storageCtx, cancel, err := o.storageContext(ctx, false)
	if err != nil {
		return s, err
	}
	defer cancel()
	current, err := o.backend.Load(storageCtx, g)
	if err != nil {
		return s, fail(StorageFailure, "commit_load")
	}
	if current.StateRevision != s.StateRevision {
		return s, fail(StaleGeneration, "save_revision")
	}
	_, expected, err := prepareCommit(current, s, current.Expected(), id)
	if err != nil {
		return s, err
	}
	if e := checkCandidateStorageBound(storageCtx); e != nil {
		return s, e
	}
	r, commitErr := o.backend.Commit(storageCtx, g, current.Expected(), s, id)
	if commitErr != nil || !receiptMatches(r, expected) {
		reconcileCtx, stop, e := o.storageContext(ctx, true)
		if e != nil {
			return s, &intentCommitError{cause: e, operationID: id}
		}
		defer stop()
		r, e = o.backend.Reconcile(reconcileCtx, g, id)
		if e == nil && r.Certainty == "not_committed" && r.OperationID == id {
			loaded, loadErr := o.backend.Load(reconcileCtx, g)
			if loadErr == nil && reconcileCtx.Err() == nil && sameSnapshot(loaded, current) {
				if _, exists := loaded.Commits[id]; !exists {
					return s, &intentCommitError{cause: fail(StorageFailure, "intent_not_committed"), operationID: id, notCommitted: true, authoritative: loaded}
				}
			}
		}
		if e != nil || !receiptMatches(r, expected) {
			return s, &intentCommitError{cause: fail(StorageFailure, "uncertain_commit"), operationID: id}
		}
		storageCtx = reconcileCtx
	}
	loaded, err := o.backend.Load(storageCtx, g)
	if err != nil || storageCtx.Err() != nil || !matchingReadback(loaded, s, expected) {
		return s, &intentCommitError{cause: fail(StorageFailure, "commit_readback"), operationID: id}
	}
	if e := checkCandidateStorageBound(storageCtx); e != nil {
		return s, &intentCommitError{cause: e, operationID: id}
	}
	return loaded, nil
}

// Durable current state, not in-memory activity or request-ID equality, owns
// uncertainty. History is not cleared by Inspect or an allowed safe read.
func durableCondition(s *Snapshot) (uncertain, recovering bool) {
	if s == nil || s.Validate() != nil || recoveryConsistency(s) != nil {
		return true, false
	}
	for _, transfer := range s.CandidateTransfers {
		if transfer != nil && (transfer.State == "reserved" || transfer.Delivery != nil && transfer.Delivery.State == "prepared" || transfer.Retirement != nil && transfer.Retirement.State == "prepared") {
			uncertain = true
		}
	}
	for key, j := range s.Journal {
		if j != nil && j.Composition != nil && j.Composition.Settlement != nil && !settlementIsVerified(s, key) {
			uncertain = true
		}
		if j != nil && j.Composition != nil && j.Composition.Stage != "done" && !settlementIsVerified(s, key) {
			recovering = true
		}
		if settledJournal(s, key) {
			continue
		}
		if j != nil {
			for _, intent := range j.Intents {
				if intent.State == "prepared" {
					uncertain = true
				}
			}
		}
		if j != nil && j.UnresolvedIntentID != "" {
			uncertain = true
		}
		if j != nil && j.Certainty == "uncertain" && j.ResolutionID == "" {
			uncertain = true
		}
	}
	for _, c := range s.Claims {
		if c == nil {
			continue
		}
		switch c.Phase {
		case "EFFECT_UNCERTAIN", "COMMIT_UNCERTAIN":
			uncertain = true
		case "RESERVED", "WAIT_READINESS", "ACQUIRING", "CANDIDATE_READY", "PROVING", "PROVED", "COMMITTING", "CANCELLED_BEFORE_EFFECT":
			recovering = true
		}
	}
	return
}
