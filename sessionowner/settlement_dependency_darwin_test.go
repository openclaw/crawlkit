//go:build darwin

package sessionowner

import (
	"context"
	"testing"
)

type dependencyFault struct {
	Backend
	key, target, mode string
	hits, reads       int
	unknown           bool
}

func (b *dependencyFault) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	s, e := b.Backend.Load(ctx, g)
	if e != nil {
		return s, e
	}
	j := s.Journal[b.key]
	if j == nil || j.Composition == nil || j.Composition.Settlement == nil {
		return s, e
	}
	r := j.Composition.Settlement
	for n, f := range r.Transactions {
		if f.ID != b.target {
			continue
		}
		switch b.mode {
		case "omit":
			r.Transactions = append(r.Transactions[:n], r.Transactions[n+1:]...)
		case "duplicate":
			r.Transactions = append(r.Transactions, f)
		case "role":
			r.Transactions[n].Role = "foreign-role"
		case "substitute":
			r.Transactions[n].ID = newID()
		case "graph-omit":
			for k, d := range r.Dependencies {
				if d.ID == f.ID {
					r.Dependencies = append(r.Dependencies[:k], r.Dependencies[k+1:]...)
					break
				}
			}
		case "graph-foreign":
			for k, d := range r.Dependencies {
				if d.ID == f.ID {
					r.Dependencies[k].Link = "foreign-owner"
					break
				}
			}
		default:
			return s, e
		}
		b.hits++
		break
	}
	return s, e
}
func (b *dependencyFault) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.target {
		b.reads++
		if b.unknown {
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
		}
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func settledAdoptedFixture(t *testing.T) (*Owner, Backend, *Channel, map[string]any, *compositionCounts, string) {
	t.Helper()
	o, b, ch, q, counts := settlementFixture(t, "retry-intent", false)
	result, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
	if e != nil || result.State != "settled" {
		t.Fatal("settled adopted setup", result, e)
	}
	key := "alice\x00" + text(q, "requestId")
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	expected := s.Expected()
	s.Provenance["later-review-metadata"] = "synthetic"
	if _, e = b.Commit(context.Background(), g, expected, s, "later-review-metadata"); e != nil {
		t.Fatal(e)
	}
	g.Close()
	return o, b, ch, q, counts, key
}
func TestB3aOriginalAdoptionCannotDisappear(t *testing.T) {
	for _, mode := range []string{"intact", "omit"} {
		t.Run(mode, func(t *testing.T) {
			o, b, _, _, counts, key := settledAdoptedFixture(t)
			s := loadHandoffSnapshot(t, b)
			adoption := ""
			for _, d := range s.Journal[key].Composition.Settlement.Dependencies {
				if d.Role == "claim_adoption" {
					adoption = d.ID
				}
			}
			if adoption == "" {
				t.Fatal("adoption dependency absent")
			}
			prior := *counts
			fault := &dependencyFault{Backend: b, key: key, target: adoption, mode: mode, unknown: true}
			fresh, ch := freshSettlement(t, o, fault)
			next := compositionRequest(fresh)
			next["expectedGeneration"] = s.Generation
			got := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, next)))
			if fault.reads != 1 || mode == "omit" && fault.hits == 0 || got["kind"] != "failed" || *counts != prior {
				t.Fatal("original adoption readback omitted", fault.reads, fault.hits, got["kind"])
			}
			if !sameSnapshot(s, loadHandoffSnapshot(t, b)) {
				t.Fatal("fault changed authority")
			}
			t.Logf("mode=%s original_adoption_reads=%d omission_hits=%d new_reads=%d result=%v", mode, fault.reads, fault.hits, counts.read-prior.read, got["kind"])
		})
	}
}
func TestB3aApplicableDependencyRows(t *testing.T) {
	for _, role := range []string{"root_finish", "intent", "child_result", "claim_adoption"} {
		for _, mode := range []string{"omit", "duplicate", "role", "substitute", "graph-omit", "graph-foreign"} {
			t.Run(role+"/"+mode, func(t *testing.T) {
				o, b, _, _, counts, key := settledAdoptedFixture(t)
				s := loadHandoffSnapshot(t, b)
				id := ""
				for _, d := range s.Journal[key].Composition.Settlement.Dependencies {
					if d.Role == role {
						id = d.ID
						break
					}
				}
				if id == "" {
					t.Fatal("role not reached")
				}
				fault := &dependencyFault{Backend: b, key: key, target: id, mode: mode}
				fresh, ch := freshSettlement(t, o, fault)
				q := compositionRequest(fresh)
				q["expectedGeneration"] = s.Generation
				before := *counts
				got := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, q)))
				if !(fault.hits > 0) || got["kind"] != "failed" || *counts != before {
					t.Fatal("dependency mutation released authority", role, mode)
				}
				if mode != "graph-omit" && mode != "graph-foreign" && fault.reads != 1 {
					t.Fatal("required original ID not reconciled")
				}
				if !sameSnapshot(s, loadHandoffSnapshot(t, b)) {
					t.Fatal("mutation persisted")
				}
			})
		}
	}
}
func TestB3aProofInvalidationAndLaterMetadata(t *testing.T) {
	o, b, _, q, _, key := settledAdoptedFixture(t)
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	if e := o.verifyReadSettlements(context.Background(), g, s); e != nil || !settlementIsVerified(s, key) {
		t.Fatal("positive later verification", e)
	}
	copy, _ := s.Clone()
	if settlementIsVerified(copy, key) {
		t.Fatal("clone kept proof")
	}
	reloaded, _ := b.Load(context.Background(), g)
	if settlementIsVerified(reloaded, key) {
		t.Fatal("reload kept proof")
	}
	if settlementIsVerified(s, "alice\x00other") {
		t.Fatal("root transplant")
	}
	revision := s.StateRevision
	s.StateRevision++
	if settlementIsVerified(s, key) {
		t.Fatal("revision transplant")
	}
	s.StateRevision = revision
	s.Provenance["mutation-after-proof"] = "changed"
	if settlementIsVerified(s, key) {
		t.Fatal("graph mutation kept proof")
	}
	g.Close()
	fresh, ch := freshSettlement(t, o, b)
	status, e := fresh.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
	if e != nil || status.State != "settled" {
		t.Fatal("legitimate later metadata rejected", status, e)
	}
}
