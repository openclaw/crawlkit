//go:build darwin

package sessionowner

import (
	"context"
	"strings"
	"testing"
	"time"
)

type outcomeContradiction struct {
	Backend
	rootKey, rootID, childKey, childID, mode string
	armed, hit                               bool
}

func (b *outcomeContradiction) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	r, e := b.Backend.Reconcile(ctx, g, id)
	if id == b.rootID {
		b.armed = true
	}
	prefix := "root-"
	target := b.rootID
	if strings.HasPrefix(b.mode, "child-receipt-") {
		prefix = "child-receipt-"
		target = b.childID
	}
	if id == target {
		switch strings.TrimPrefix(b.mode, prefix) {
		case "id":
			b.hit = true
			r.OperationID = newID()
		case "digest":
			b.hit = true
			r.Digest = strings.Repeat("0", 64)
		case "generation":
			b.hit = true
			r.Generation = "other-generation"
		case "revision":
			b.hit = true
			r.StateRevision++
		case "noncommit":
			b.hit = true
			r.Certainty = "not_committed"
		}
	}
	return r, e
}
func (b *outcomeContradiction) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	s, e := b.Backend.Load(ctx, g)
	if e != nil || !b.armed {
		return s, e
	}
	root := s.Journal[b.rootKey]
	child := s.Journal[b.childKey]
	changed := true
	switch b.mode {
	case "missing-root":
		delete(s.Journal, b.rootKey)
	case "root-finish-id":
		root.Composition.FinishID = newID()
	case "root-link":
		root.Composition.RetryID = newID()
	case "journal-digest":
		root.Digest = strings.Repeat("0", 64)
	case "root-operation":
		root.OperationID = "different.operation"
	case "root-caller":
		root.Caller = "bob"
	case "root-payload":
		root.Response = []byte(`{}`)
	case "root-pending-payload":
		root.Composition.FinalPayload = []byte(`{}`)
	case "snapshot-generation":
		s.Generation = "other-generation"
	case "snapshot-revision":
		s.StateRevision++
	case "missing-root-receipt":
		delete(s.Commits, b.rootID)
	case "child-link":
		child.CompositionKey = "foreign-root"
	case "child-id":
		child.ResponseCommitID = newID()
	case "child-payload":
		child.Response = []byte(`{}`)
	case "child-pending":
		child.PendingResponse = []byte(`{}`)
	case "child-generation":
		child.Generation = "foreign-generation"
	case "missing-child":
		delete(s.Journal, b.childKey)
	default:
		changed = false
	}
	b.hit = b.hit || changed
	return s, e
}
func TestB2B2AContradictoryOutcomeReadback(t *testing.T) {
	modes := []string{"root-id", "root-digest", "root-generation", "root-revision", "root-noncommit", "missing-root", "root-finish-id", "journal-digest", "root-link", "root-operation", "root-caller", "root-payload", "root-pending-payload", "snapshot-generation", "snapshot-revision", "missing-root-receipt", "child-link", "child-id", "child-payload", "child-pending", "child-generation", "missing-child", "child-receipt-id", "child-receipt-digest", "child-receipt-generation", "child-receipt-revision", "child-receipt-noncommit"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, _, counts := compositionFixture(t, "recover")
			q := compositionRequest(o)
			good := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
			if good["kind"] != "performed" {
				t.Fatal("positive setup failed")
			}
			s := loadHandoffSnapshot(t, b)
			key := "alice\x00" + text(q, "requestId")
			root := s.Journal[key]
			childKey := "alice\x00" + root.Composition.RetryID
			fault := &outcomeContradiction{Backend: b, rootKey: key, rootID: root.Composition.FinishID, childKey: childKey, childID: s.Journal[childKey].ResponseCommitID, mode: mode}
			fresh, c2 := reopenComposition(t, o, fault)
			before := *counts
			got := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
			if !fault.hit || got["kind"] != "failed" || *counts != before {
				t.Fatal("contradictory outcome accepted or extra effects", mode, fault.hit, got["kind"])
			}
			if !sameSnapshot(s, loadHandoffSnapshot(t, b)) {
				t.Fatal("readback mutated authority")
			}
		})
	}
}

type cancelFinalLoad struct {
	Backend
	trigger func()
	hit     bool
}

func (b *cancelFinalLoad) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	s, e := b.Backend.Load(ctx, g)
	if e == nil && !b.hit {
		for _, j := range s.Journal {
			if j.Composition != nil && j.Composition.Stage == "done" {
				b.hit = true
				b.trigger()
				break
			}
		}
	}
	return s, e
}
func TestB2B2AFinalDeliveryPrecedence(t *testing.T) {
	for _, boundary := range []string{"final-load", "live_readback", "live_return", "cache_return", "default-cache", "history_return"} {
		for _, expiry := range []bool{false, true} {
			name := boundary + "/cancel"
			if expiry {
				name = boundary + "/expiry"
			}
			t.Run(name, func(t *testing.T) {
				o, b, ch, clock, counts := compositionFixture(t, "recover")
				q := compositionRequest(o)
				deadline, _ := time.Parse("2006-01-02T15:04:05.000Z", text(q, "deadline"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				hit := false
				trigger := func() {
					hit = true
					if expiry {
						clock.Advance(deadline)
					} else {
						cancel()
					}
				}
				var activeOwner *Owner = o
				activeChannel := ch
				if boundary == "cache_return" || boundary == "default-cache" || boundary == "history_return" {
					good := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
					if good["kind"] != "performed" {
						t.Fatal("setup")
					}
					if boundary == "history_return" {
						activeOwner, activeChannel = reopenComposition(t, o, b)
					}
				}
				if boundary == "final-load" {
					activeOwner.backend = &cancelFinalLoad{Backend: b, trigger: trigger}
				} else {
					activeOwner.compositionBoundary = func(point string) {
						if (point == boundary || boundary == "default-cache" && point == "cache_return") && !hit {
							trigger()
						}
					}
				}
				var delivered []byte
				var deliveryErr error
				if boundary == "default-cache" {
					delivered, deliveryErr = activeOwner.Handle(ctx, activeChannel, rawJSON(t, q))
				} else {
					delivered, deliveryErr = activeOwner.HandleWithRecovery(ctx, activeChannel, rawJSON(t, q))
				}
				got := resultMap(t)(delivered, deliveryErr)
				if !hit || got["kind"] != "failed" || got["certainty"] == "not_dispatched" || counts.read != 2 || counts.acquire != 1 || counts.proof != 1 {
					t.Fatal("delivery cancellation boundary", boundary, hit, got["kind"], got["certainty"], *counts)
				}
				if boundary != "final-load" && got["certainty"] != "completed" {
					t.Fatal("known committed history was discarded")
				}
				state := loadHandoffSnapshot(t, b)
				root := state.Journal["alice\x00"+text(q, "requestId")]
				if root.Composition.Stage != "done" || len(root.Response) == 0 {
					t.Fatal("cancellation erased committed history")
				}
				before := *counts
				fresh, c2 := reopenComposition(t, o, b)
				q["deadline"] = timestamp(clock.Now().Add(time.Hour))
				history := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
				if history["kind"] != "performed" || *counts != before || history["receiptRef"] != root.ReceiptID {
					t.Fatal("historical readback failed or replayed")
				}
			})
		}
	}
}
func TestB2B2AProtectedHistoricalOutcome(t *testing.T) {
	o, memory, ch, _, counts := compositionFixture(t, "recover")
	file, dir := fileFixture(t)
	initial := loadHandoffSnapshot(t, memory)
	g, _ := file.Acquire(context.Background())
	empty, _ := file.Load(context.Background(), g)
	file.Commit(context.Background(), g, empty.Expected(), initial, "outcome-enrollment")
	g.Close()
	o.backend = file
	q := compositionRequest(o)
	good := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
	if good["kind"] != "performed" {
		t.Fatal("setup")
	}
	g, _ = file.Acquire(context.Background())
	s, _ := file.Load(context.Background(), g)
	expected := s.Expected()
	s.Provenance["syntheticAudit"] = "later-metadata"
	if _, e := file.Commit(context.Background(), g, expected, s, "later-metadata"); e != nil {
		t.Fatal(e)
	}
	g.Close()
	freshFile, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		t.Fatal(e)
	}
	fresh, c2 := reopenComposition(t, o, freshFile)
	before := *counts
	history := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
	if !equalJSON(good, history) || *counts != before {
		t.Fatal("later-revision historical result changed")
	}
}
