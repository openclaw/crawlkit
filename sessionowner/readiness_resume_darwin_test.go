//go:build darwin

package sessionowner

import (
	"context"
	"testing"
)

func c5ReopenReady(t *testing.T, old *Owner, dir string) (*Owner, *FileBackend, *Channel) {
	t.Helper()
	file, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		t.Fatal("approved protected-temp reopen failed", e)
	}
	fresh, e := NewOwner(OwnerOptions{Registry: old.registry, Backend: file, Authorizer: fixtureAuth{}, Adapters: old.adapters, Proof: old.proof, WarmReady: func() bool { return false }})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(old, fresh)
	ch, e := fresh.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(ch.Close)
	return fresh, file, ch
}
func TestC5R2ColdReadinessProtectedReopen(t *testing.T) {
	// Scenario1: real readiness pause, actual guarded disk readback and reopen.
	file, dir := fileFixture(t)
	o, b, ch, clock, trace := c5ReadyOwner(t, file, false)
	paused, id := c5ActualPause(t, o, b, ch, trace)
	ch.Close()
	fresh, reopened, newChannel := c5ReopenReady(t, o, dir)
	observed := loadHandoffSnapshot(t, reopened)
	if !sameSnapshot(paused, observed) || payloadDigest(paused.Claims[id]) != payloadDigest(observed.Claims[id]) {
		t.Fatal("protected readiness reopen lost original claim/evidence/counters/fence")
	}
	t.Logf("C5_CONTROL protected_reopen: exact claim=%s origin=%s resolution=%s checkpointTransaction=%s retained", id, observed.Claims[id].OriginRequestID, observed.Journal[observed.Claims[id].OriginJournalKey].ResolutionID, trace.ackReceipts[0].OperationID)
	c5Continue(t, fresh, reopened, newChannel, clock, trace, paused, id, false)
	// Scenario2 is a separately reported control inside this leaf: a real
	// readiness permit/lost outcome, never inferred no-effect from an error.
	uncertainFile, uncertainDir := fileFixture(t)
	u, ub, uch, uClock, uTrace := c5ReadyOwner(t, uncertainFile, true)
	got := resultMap(t)(u.Handle(context.Background(), uch, rawJSON(t, b2Request("new", ""))))
	uid := b2ClaimID(t, got)
	uncertain := loadHandoffSnapshot(t, ub)
	claim := uncertain.Claims[uid]
	origin := uncertain.Journal[claim.OriginJournalKey]
	if got["kind"] != "failed" || text(mapValue(got["failure"]), "category") != string(ReconciliationRequired) || got["certainty"] != "uncertain" || uTrace.readiness != 1 || uTrace.acquisition != 0 || uTrace.proof != 0 || claim.Phase != "EFFECT_UNCERTAIN" || claim.NoEffectProved || claim.ActiveStep != "readiness" || len(claim.EffectIDs) != 1 || claim.AcquisitionAttempts != 0 || claim.ProofAttempts != 0 || origin.Dispatches != 1 || len(origin.Intents) != 1 || len(origin.EffectIDs) != 1 || origin.ResolutionID != "" {
		t.Fatal("actual uncertain-readiness permit control not reached", got["kind"], uTrace.steps)
	}
	uch.Close()
	uFresh, uReopened, uChannel := c5ReopenReady(t, u, uncertainDir)
	if !sameSnapshot(uncertain, loadHandoffSnapshot(t, uReopened)) {
		t.Fatal("uncertain original readiness state not durable")
	}
	uClock.Advance(uncertain.Fences["recovery"])
	refused := resultMap(t)(uFresh.Handle(context.Background(), uChannel, rawJSON(t, b2Request("resume", uid))))
	after := loadHandoffSnapshot(t, uReopened)
	if refused["kind"] != "failed" || text(mapValue(refused["failure"]), "category") != string(ReconciliationRequired) || refused["certainty"] != "uncertain" || uTrace.readiness != 1 || uTrace.acquisition != 0 || uTrace.proof != 0 || uTrace.ordinary != 0 || payloadDigest(after.Claims[uid]) != payloadDigest(claim) || payloadDigest(after.Journal[claim.OriginJournalKey]) != payloadDigest(origin) || !after.Fences["recovery"].Equal(uncertain.Fences["recovery"]) {
		t.Fatal("unproved readiness resumed/refunded/changed original history", refused["kind"], uTrace.steps)
	}
	for key, receipt := range uncertain.Commits {
		if !receiptMatches(after.Commits[key], receipt) {
			t.Fatal("uncertain readiness original receipt replaced")
		}
	}
	t.Logf("C5_CONTROL uncertain_readiness: actualPermit=1 claim=%s origin=%s effect=%s authoritativeReconcilerAbsent refusal=reconciliation_required callbacksAfterResume=0 acquisition=0 proof=0", uid, claim.OriginRequestID, claim.EffectIDs[0])
}
