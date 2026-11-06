package sessionowner

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func b2a1State(phase string) *Snapshot {
	s := NewSnapshot("fixture-binding")
	s.Generation = "epoch-1"
	s.StateRevision = 10
	s.Credentials = map[string]any{"token": "synthetic-private"}
	c := &Claim{ID: "state-claim", Epoch: "epoch-1", BindingID: s.BindingID, Caller: "alice", Cause: "authorized_replacement", OriginRequestID: "origin", OriginJournalKey: "alice\x00origin", Phase: phase}
	j := &JournalEntry{Caller: "alice", RequestID: "origin", Generation: "epoch-1", RequestKind: "recover", ClaimID: c.ID, Semantics: "recovery", Certainty: "not_dispatched", Phase: "EXECUTE_READY", ReceiptID: "receipt"}
	s.Claims[c.ID] = c
	s.Journal[c.OriginJournalKey] = j
	candidate := func() {
		c.AcquisitionAttempts = 1
		c.Candidate = map[string]any{"token": "retained"}
		c.CandidateDigest = payloadDigest(c.Candidate)
		c.CandidateExpires = time.Now().Add(time.Hour)
		j.Dispatches = 1
		j.EffectIDs = []string{"capture"}
		j.Certainty = "completed"
		c.NoEffectProved = true
	}
	proof := func() {
		candidate()
		c.ProofAttempts = 1
		c.Proof = map[string]any{"account": "expected"}
		c.ProofDigest = payloadDigest(c.Proof)
		c.ProofCandidateDigest = c.CandidateDigest
		j.Dispatches = 2
		j.EffectIDs = append(j.EffectIDs, "proof")
	}
	switch phase {
	case "WAIT_READINESS", "CANCELLED_BEFORE_EFFECT":
		c.NoEffectProved = true
	case "ACQUIRING":
		c.ActiveStep = "acquire_candidate"
		c.ActiveJournalKey = c.OriginJournalKey
		c.NoEffectProved = true
	case "CANDIDATE_READY":
		candidate()
	case "PROVING":
		candidate()
		c.ActiveStep = "prove_candidate"
		c.ActiveJournalKey = c.OriginJournalKey
	case "PROVED":
		proof()
	case "EFFECT_UNCERTAIN":
		c.ActiveStep = "readiness"
		c.ActiveJournalKey = c.OriginJournalKey
		c.EffectIDs = []string{"readiness"}
		j.EffectIDs = []string{"readiness"}
		j.Dispatches = 1
		j.Certainty = "uncertain"
	case "COMMITTING", "COMMIT_UNCERTAIN", "ACCEPTED":
		proof()
		c.CommitAttempts = 1
		c.CommitOperationID = "commit"
		c.CommitRequestID = "origin"
		c.CommitBaseRevision = 8
		c.ProposedGeneration = "epoch-2"
		if phase == "ACCEPTED" {
			s.Generation = "epoch-2"
			r := map[string]any{"protocol": Protocol, "kind": "recovered", "requestId": "origin", "bindingId": s.BindingID, "acceptedGeneration": c.Epoch, "finalGeneration": c.ProposedGeneration, "stateRevision": "9", "receiptRef": "receipt", "certainty": "completed", "nextAction": "none", "retryNotBefore": nil, "claim": claimStatus(c)}
			raw, _ := json.Marshal(r)
			c.AcceptedOutcome = raw
			j.Response = raw
			s.Commits[c.CommitOperationID] = CommitReceipt{OperationID: c.CommitOperationID, Certainty: "committed", Generation: c.ProposedGeneration, StateRevision: 9, Digest: strings.Repeat("a", 64)}
		}
	}
	return s
}
func TestB2A1AllSupportedPersistentPhases(t *testing.T) {
	for _, phase := range []string{"RESERVED", "WAIT_READINESS", "ACQUIRING", "CANDIDATE_READY", "PROVING", "PROVED", "COMMITTING", "REJECTED", "EFFECT_UNCERTAIN", "COMMIT_UNCERTAIN", "ACCEPTED", "CANCELLED_BEFORE_EFFECT"} {
		t.Run(phase, func(t *testing.T) {
			s := b2a1State(phase)
			if !supportedClaimPhase(phase) {
				t.Fatal("missing supported phase")
			}
			if e := recoveryConsistency(s); e != nil {
				t.Fatal("valid state rejected", e)
			}
		})
	}
}
func TestB2A1MalformedStateCannotEscapeAdmission(t *testing.T) {
	tests := []struct {
		name, phase string
		mutate      func(*Snapshot, *Claim)
	}{
		{"unknown", "CANDIDATE_READY", func(s *Snapshot, c *Claim) { c.Phase = "DO_NOT_ECHO_PRIVATE_PHASE" }}, {"empty", "RESERVED", func(s *Snapshot, c *Claim) { c.Phase = "" }}, {"padded", "RESERVED", func(s *Snapshot, c *Claim) { c.Phase = " RESERVED" }},
		{"negative-resume", "RESERVED", func(s *Snapshot, c *Claim) { c.ReadinessResumptions = -1 }}, {"excess-resume", "RESERVED", func(s *Snapshot, c *Claim) { c.ReadinessResumptions = 3 }}, {"negative-acquire", "RESERVED", func(s *Snapshot, c *Claim) { c.AcquisitionAttempts = -1 }}, {"excess-acquire", "CANDIDATE_READY", func(s *Snapshot, c *Claim) { c.AcquisitionAttempts = 2 }}, {"negative-proof", "CANDIDATE_READY", func(s *Snapshot, c *Claim) { c.ProofAttempts = -1 }}, {"excess-proof", "PROVED", func(s *Snapshot, c *Claim) { c.ProofAttempts = 2 }},
		{"candidate-hash", "CANDIDATE_READY", func(s *Snapshot, c *Claim) { c.CandidateDigest = "wrong" }}, {"proof-without-candidate", "PROVED", func(s *Snapshot, c *Claim) { c.Candidate = nil }}, {"proof-hash", "PROVED", func(s *Snapshot, c *Claim) { c.ProofDigest = "wrong" }}, {"commit-without-attempt", "COMMITTING", func(s *Snapshot, c *Claim) { c.CommitAttempts = 0 }}, {"future-commit-base", "COMMITTING", func(s *Snapshot, c *Claim) { c.CommitBaseRevision = 100 }}, {"missing-committed-receipt", "ACCEPTED", func(s *Snapshot, c *Claim) { delete(s.Commits, c.CommitOperationID) }},
		{"foreign-effect", "EFFECT_UNCERTAIN", func(s *Snapshot, c *Claim) { c.EffectIDs = []string{"not-journaled"} }}, {"negative-journal", "RESERVED", func(s *Snapshot, c *Claim) { s.Journal[c.OriginJournalKey].Dispatches = -1 }}, {"permit-no-count", "ACQUIRING", func(s *Snapshot, c *Claim) {
			c.EffectIDs = []string{"effect"}
			c.NoEffectProved = false
			s.Journal[c.OriginJournalKey].EffectIDs = []string{"effect"}
			s.Journal[c.OriginJournalKey].Dispatches = 1
		}}, {"unsupported-step", "RESERVED", func(s *Snapshot, c *Claim) { c.ActiveStep = "arbitrary"; c.ActiveJournalKey = c.OriginJournalKey }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := b2a1State(tc.phase)
			tc.mutate(s, s.Claims["state-claim"])
			if e := recoveryConsistency(s); e == nil {
				t.Fatal("inconsistent state qualified")
			}
			before, _ := json.Marshal(s)
			b, e := NewMemoryBackend(s)
			if e != nil {
				t.Fatal(e)
			}
			var effects int
			template, _, _ := fixtureOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) { effects++; return AdapterOutput{}, nil })
			o, e := NewOwner(OwnerOptions{Registry: template.registry, Backend: b, Authorizer: fixtureAuth{}, Adapters: template.adapters, UncertainReadOperations: []OperationRef{{ID: "fixture.read", Version: 1}}})
			if e != nil {
				t.Fatal(e)
			}
			c, _ := o.AuthenticateChannel(context.Background(), "alice")
			status := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("inspect"))))
			if status["state"] != "uncertain" {
				t.Fatal("inconsistent state reported safe")
			}
			encoded, _ := json.Marshal(status)
			if bytes.Contains(encoded, []byte("DO_NOT_ECHO")) {
				t.Fatal("raw phase reflected")
			}
			for _, kind := range []string{"fixture.read", "fixture.write"} {
				q := fixtureRequest("perform")
				q["expectedGeneration"] = s.Generation
				q["operationId"] = kind
				if kind == "fixture.write" {
					q["confirmationRef"] = "approval-1"
				}
				got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
				if got["kind"] != "failed" || effects != 0 {
					t.Fatal("inconsistent state dispatched")
				}
			}
			g, _ := b.Acquire(context.Background())
			after, _ := b.Load(context.Background(), g)
			g.Close()
			data, _ := json.Marshal(after)
			if !bytes.Equal(before, data) {
				t.Fatal("invalid state normalized or cleared")
			}
		})
	}
}
