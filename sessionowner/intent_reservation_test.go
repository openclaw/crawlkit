package sessionowner

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Fault selectors describe actual semantic transitions, never Commit ordinals.
func a4TransactionStage(current, next *Snapshot, id string) string {
	for key, j := range next.Journal {
		if j == nil {
			continue
		}
		var prior *JournalEntry
		if current != nil {
			prior = current.Journal[key]
		}
		for intentID, r := range j.Intents {
			var old IntentReservation
			ok := false
			if prior != nil {
				old, ok = prior.Intents[intentID]
			}
			if intentID == id && r.State == "committed" && ok && old.State == "prepared" {
				return "intent"
			}
			if r.State == "prepared" && !ok {
				return "reservation"
			}
		}
	}
	return "other"
}

type a4FaultBackend struct {
	Backend
	reserveMode, intentMode                                 string
	reservationHits, intentHits, cleanupHits, originalReads int
	intentID, reservationID                                 string
	initial, denyCleanup                                    bool
	resolve                                                 string
}

func (b *a4FaultBackend) Commit(ctx context.Context, g *Guard, e Expected, n *Snapshot, id string) (CommitReceipt, error) {
	current, err := b.Backend.Load(ctx, g)
	if err != nil {
		return CommitReceipt{}, err
	}
	stage := a4TransactionStage(current, n, id)
	if stage == "reservation" {
		b.reservationHits++
		if b.reservationID == "" {
			b.reservationID = id
			for _, j := range n.Journal {
				for key, r := range j.Intents {
					if r.State == "prepared" {
						b.intentID = key
					}
				}
			}
			switch b.reserveMode {
			case "not_committed":
				return CommitReceipt{OperationID: id, Certainty: "not_committed"}, nil
			case "uncertain":
				return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
			case "uncertain_stored":
				if _, err = b.Backend.Commit(ctx, g, e, n, id); err != nil {
					return CommitReceipt{}, err
				}
				return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
			}
		}
	}
	if stage == "intent" {
		b.intentHits++
		if b.initial && id == b.intentID {
			switch b.intentMode {
			case "uncertain":
				return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
			case "uncertain_stored":
				if _, err = b.Backend.Commit(ctx, g, e, n, id); err != nil {
					return CommitReceipt{}, err
				}
				return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
			case "not_committed":
				return CommitReceipt{OperationID: id, Certainty: "not_committed"}, nil
			}
		}
	}
	if b.initial && b.denyCleanup && b.intentHits > 0 && stage == "other" {
		b.cleanupHits++
		return CommitReceipt{OperationID: id, Certainty: "not_committed"}, nil
	}
	return b.Backend.Commit(ctx, g, e, n, id)
}
func (b *a4FaultBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.reservationID && (b.reserveMode == "uncertain" || b.reserveMode == "uncertain_stored") && b.initial {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	if id == b.intentID {
		b.originalReads++
		if b.resolve == "uncertain" {
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
		}
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func a4Owner(t *testing.T) (*Owner, *MemoryBackend, *Channel, *int, *int) {
	effects, proofs := 0, 0
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			if err := i.BeforeEffect(ctx, "ACQUIRING"); err != nil {
				return AdapterOutput{}, err
			}
			effects++
			return AdapterOutput{Candidate: map[string]any{"token": "a4-new-candidate"}, CandidateExpires: time.Now().Add(time.Hour)}, nil
		}
		if err := i.BeforeEffect(ctx, "PROVING"); err != nil {
			return AdapterOutput{}, err
		}
		proofs++
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	})
	return o, b, c, &effects, &proofs
}
func a4Claim(t *testing.T, b Backend) *Claim {
	t.Helper()
	g, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	s, e := b.Load(context.Background(), g)
	g.Close()
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range s.Claims {
		return c
	}
	t.Fatal("claim missing")
	return nil
}
func a4Restart(t *testing.T, o *Owner, b Backend) (*Owner, *Channel) {
	t.Helper()
	fresh, e := NewOwner(OwnerOptions{Registry: o.registry, Backend: b, Authorizer: fixtureAuth{}, Adapters: o.adapters, Proof: o.proof})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(o, fresh)
	c, e := fresh.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	return fresh, c
}
func TestA4ReservationMustBePositiveBeforeIntent(t *testing.T) {
	for _, mode := range []string{"not_committed", "uncertain", "uncertain_stored"} {
		t.Run(mode, func(t *testing.T) {
			o, b, c, effects, _ := a4Owner(t)
			fault := &a4FaultBackend{Backend: b, reserveMode: mode, initial: true}
			o.backend = fault
			got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
			if fault.reservationHits != 1 || fault.intentHits != 0 || *effects != 0 || got["kind"] == "recovered" {
				t.Fatal("nonpositive preparation reached actual intent")
			}
			cl := a4Claim(t, b)
			if cl.AcquisitionAttempts != 0 {
				t.Fatal("reservation consumed quota")
			}
			fault.initial = false
			fresh, ch := a4Restart(t, o, fault)
			resumed := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID))))
			if resumed["kind"] != "recovered" || *effects != 1 {
				t.Fatal("legitimate never-attempted resume blocked", resumed["kind"])
			}
		})
	}
}
func TestA4OriginalUnknownSurvivesFailedCleanupAndRestart(t *testing.T) {
	o, b, c, effects, proofs := a4Owner(t)
	fault := &a4FaultBackend{Backend: b, intentMode: "uncertain", initial: true, denyCleanup: true, resolve: "uncertain"}
	o.backend = fault
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	if fault.reservationHits != 1 || fault.intentHits != 1 || *effects != 0 || first["certainty"] != "uncertain" {
		t.Fatal("target intent uncertainty not exercised")
	}
	cl := a4Claim(t, b)
	if cl.AcquisitionAttempts != 0 {
		t.Fatal("unknown proposal persisted as consumed")
	}
	beforeReads := fault.originalReads
	fault.initial = false
	fresh, ch := a4Restart(t, o, fault)
	refused := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID))))
	if refused["kind"] != "failed" || *effects != 0 || fault.originalReads <= beforeReads || fault.intentHits != 1 {
		t.Fatal("restart bypassed exact original unknown intent")
	}
	fault.resolve = "not_committed"
	recovered := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID))))
	if recovered["kind"] != "recovered" || *effects != 1 || *proofs != 1 {
		t.Fatal("definitive original noncommit did not resume")
	}
}
func TestA4CommittedUnknownNeverRefundsPermit(t *testing.T) {
	o, b, c, effects, _ := a4Owner(t)
	fault := &a4FaultBackend{Backend: b, intentMode: "uncertain_stored", initial: true, denyCleanup: true, resolve: "uncertain"}
	o.backend = fault
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	if first["certainty"] != "uncertain" || *effects != 0 || fault.intentHits != 1 {
		t.Fatal("committed unknown setup")
	}
	cl := a4Claim(t, b)
	if cl.AcquisitionAttempts != 1 || len(cl.EffectIDs) != 1 {
		t.Fatal("committed quota lost")
	}
	fault.initial = false
	fault.resolve = "committed"
	fresh, ch := a4Restart(t, o, fault)
	got := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID))))
	if got["kind"] != "failed" || *effects != 0 || a4Claim(t, b).AcquisitionAttempts != 1 {
		t.Fatal("issued witness refunded or acquisition replayed")
	}
}
func TestA4FailedCleanupKeepsOriginalIntentDiscoverable(t *testing.T) {
	o, b, c, _, _ := a4Owner(t)
	original := o.adapters["fixture"].(fixtureAdapter)
	o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			if err := i.BeforeEffect(ctx, "ACQUIRING"); err != nil {
				return AdapterOutput{}, err
			}
			return AdapterOutput{}, errors.New("lost result")
		}
		return original.Execute(ctx, i)
	}}
	fault := &a4FaultBackend{Backend: b, initial: true, denyCleanup: true}
	o.backend = fault
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	if got["certainty"] != "uncertain" || fault.cleanupHits == 0 || fault.intentHits != 1 {
		t.Fatal("cleanup failure stage not exercised")
	}
	cl := a4Claim(t, b)
	fault.initial = false
	before := fault.originalReads
	fresh, ch := a4Restart(t, o, fault)
	fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID)))
	if fault.originalReads <= before || a4Claim(t, b).AcquisitionAttempts != 1 {
		t.Fatal("cleanup failure erased original intent")
	}
}
