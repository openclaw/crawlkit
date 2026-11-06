//go:build darwin

package sessionowner

import (
	"context"
	"testing"
)

// Alter only observations, never the underlying authority or its receipts.
type outcomeRevisionView struct {
	Backend
	mode, rootID     string
	loads, rootReads int
	returned         []uint64
}

func (b *outcomeRevisionView) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	s, e := b.Backend.Load(ctx, g)
	if e != nil {
		return s, e
	}
	b.loads++
	if b.mode == "earlier" {
		s.StateRevision--
	}
	if b.mode == "same-provenance" {
		s.Provenance["uncommitted-change"] = "synthetic"
	}
	b.returned = append(b.returned, s.StateRevision)
	return s, nil
}
func (b *outcomeRevisionView) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.rootID {
		b.rootReads++
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func TestB2B2BRootRevisionObservations(t *testing.T) {
	for _, mode := range []string{"equal", "same-provenance", "earlier", "later"} {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, _, counts := compositionFixture(t, "recover")
			q := compositionRequest(o)
			good := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
			if good["kind"] != "performed" {
				t.Fatal("setup")
			}
			key := "alice\x00" + text(q, "requestId")
			before := loadHandoffSnapshot(t, b)
			root := before.Journal[key]
			receipt := before.Commits[root.Composition.FinishID]
			if before.StateRevision != receipt.StateRevision {
				t.Fatal("not exact committed setup")
			}
			if mode == "later" {
				g, _ := b.Acquire(context.Background())
				s, _ := b.Load(context.Background(), g)
				expected := s.Expected()
				s.Provenance["committed-later"] = "synthetic"
				if _, e := b.Commit(context.Background(), g, expected, s, "later-root-metadata"); e != nil {
					t.Fatal(e)
				}
				g.Close()
				before = loadHandoffSnapshot(t, b)
			}
			view := &outcomeRevisionView{Backend: b, mode: mode, rootID: root.Composition.FinishID}
			fresh, c2 := reopenComposition(t, o, view)
			previous := *counts
			got := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
			if view.loads != 2 || view.rootReads != 1 || len(view.returned) != 2 {
				t.Fatal("target stages not hit", view.loads, view.rootReads)
			}
			want := receipt.StateRevision
			if mode == "earlier" {
				want--
			}
			if mode == "later" {
				want++
			}
			for _, revision := range view.returned {
				if revision != want {
					t.Fatal("injected revision not observed", revision, want)
				}
			}
			if mode == "equal" || mode == "later" {
				if !equalJSON(good, got) {
					t.Fatal("valid current/later history rejected", mode)
				}
			} else if got["kind"] != "failed" {
				t.Fatal("inconsistent revision accepted", mode)
			}
			if *counts != previous || !sameSnapshot(before, loadHandoffSnapshot(t, b)) {
				t.Fatal("readback changed work/history")
			}
			t.Logf("mode=%s observed=%v committed=%d loads=%d reconciles=%d kind=%v", mode, view.returned, receipt.StateRevision, view.loads, view.rootReads, got["kind"])
		})
	}
}
func TestB2B2BRequiredChildRevisionBounds(t *testing.T) {
	o, b, ch, _, counts := compositionFixture(t, "recover")
	q := compositionRequest(o)
	got := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
	if got["kind"] != "performed" {
		t.Fatal("setup")
	}
	original := loadHandoffSnapshot(t, b)
	rootKey := "alice\x00" + text(q, "requestId")
	root := original.Journal[rootKey]
	previous := *counts
	for _, kind := range []string{"initial", "recovery", "retry"} {
		for _, relative := range []string{"earlier", "equal", "later"} {
			t.Run(kind+"/"+relative, func(t *testing.T) {
				s, _ := original.Clone()
				r := s.Journal[rootKey]
				id := r.Composition.InitialID
				if kind == "recovery" {
					id = r.Composition.RecoveryID
				}
				if kind == "retry" {
					id = r.Composition.RetryID
				}
				child := s.Journal[r.Caller+"\x00"+id]
				commitID := child.ResponseCommitID
				if kind == "recovery" {
					commitID = s.Claims[child.ClaimID].CommitOperationID
				}
				receipt, ok := s.Commits[commitID]
				if !ok || receipt.Certainty != "committed" {
					t.Fatal("positive child receipt missing")
				}
				s.StateRevision = receipt.StateRevision
				if relative == "earlier" {
					s.StateRevision--
				}
				if relative == "later" {
					s.StateRevision++
				}
				g, e := b.Acquire(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				out, e := o.compositionChildWitness(context.Background(), g, s, rootKey, r, child)
				g.Close()
				if relative == "earlier" {
					if e == nil || out != nil {
						t.Fatal("child preceding receipt accepted")
					}
				} else if e != nil || out == nil {
					t.Fatal("valid child bound refused", e)
				}
				if *counts != previous || !sameSnapshot(original, loadHandoffSnapshot(t, b)) {
					t.Fatal("child witness changed authority")
				}
			})
		}
	}
	// The global admission witness has the same lower bound before unrelated work.
	stale, _ := original.Clone()
	stale.StateRevision = original.Commits[root.Composition.FinishID].StateRevision - 1
	g, _ := b.Acquire(context.Background())
	e := o.verifyCompositionOutcomes(context.Background(), g, stale)
	g.Close()
	if e == nil {
		t.Fatal("global committed witness accepted preceding snapshot")
	}
}
func TestB2B2BProtectedLaterHistory(t *testing.T) {
	o, memory, ch, _, counts := compositionFixture(t, "recover")
	file, dir := fileFixture(t)
	initial := loadHandoffSnapshot(t, memory)
	g, _ := file.Acquire(context.Background())
	empty, _ := file.Load(context.Background(), g)
	if _, e := file.Commit(context.Background(), g, empty.Expected(), initial, "revision-enrollment"); e != nil {
		t.Fatal(e)
	}
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
	s.Provenance["metadata-after-result"] = "synthetic"
	if _, e := file.Commit(context.Background(), g, expected, s, "later-file-metadata"); e != nil {
		t.Fatal(e)
	}
	g.Close()
	reopened, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		t.Fatal(e)
	}
	fresh, c2 := reopenComposition(t, o, reopened)
	before := *counts
	history := resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
	if !equalJSON(good, history) || *counts != before {
		t.Fatal("protected strictly-later history failed")
	}
}
