//go:build darwin

package sessionowner

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func stagedSettlementGraph(t *testing.T) (*Owner, Backend, *Channel, map[string]any, *compositionCounts, string) {
	t.Helper()
	o, b, ch, _, counts := compositionFixture(t, "recover")
	o.registry = transferRegistry(t, 8192, 4096)
	for key, op := range o.registry.operations {
		op = cloneOperation(op)
		if op.RequestKind == "perform" && op.Semantics == "safe_read" {
			for n := range op.BudgetPlans {
				p := &op.BudgetPlans[n]
				p.MaxAcquisitionAttempts = 1
				p.MaxProofAttempts = 1
				p.MaxReadinessResumptions = 2
				p.PhaseMaxMs["capture"] = 500
				p.PhaseMaxMs["proof"] = 500
			}
		}
		o.registry.operations[key] = op
		if op.RequestKind != "perform" {
			o.registry.builtins[op.RequestKind] = op
		}
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	files, e := NewProtectedCandidateFiles(dir, "fixture-binding")
	if e != nil {
		t.Fatal(e)
	}
	o.candidateBindings = map[string]CandidateBinding{"fixture": {Repository: files, Expiry: ProtectedProducerExpiry}}
	payload := rawJSON(t, map[string]any{"token": strings.Repeat("s", 6000)})
	expiry := o.now().Add(time.Hour)
	base := o.adapters["fixture"].(fixtureAdapter)
	proofCalls := 0
	o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "execute" {
			return base.Execute(ctx, i)
		}
		session, e := NewAdapterSession(i, "staged-settlement-parent")
		if e != nil {
			return AdapterOutput{}, e
		}
		if i.Step() == "acquire_candidate" {
			counts.acquire++
			return serveCandidatePeer(t, ctx, i, session, files, payload, expiry, "staged")
		}
		proofCalls++
		if proofCalls == 1 {
			frame, e := session.InvokeFrame()
			if e != nil {
				return AdapterOutput{}, e
			}
			v, _ := DecodeJSON(frame, HardFrameLimit)
			m, _ := object(v)
			wire, _ := object(m["candidate"])
			if text(wire, "mode") != "staged" {
				t.Fatal("outbound stage not reached")
			}
			if _, e = session.ConsumeStagedCandidate(ctx, text(wire, "candidateRef"), int(num(wire["bytes"])), text(wire, "privateDigest")); e != nil {
				return AdapterOutput{}, e
			}
			session.ParentLost()
			return AdapterOutput{}, fail(Canceled, "pause-before-proof")
		}
		counts.proof++
		return serveCandidatePeer(t, ctx, i, session, files, payload, expiry, "staged")
	}}
	stop := &composedOutcomeFault{Backend: b, target: "final", stored: false, denyLater: true}
	o.backend = stop
	q := compositionRequest(o)
	first := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
	if first["kind"] != "failed" || !stop.hit || counts.acquire != 1 || counts.proof != 0 {
		t.Fatal("staged pause not reached", first["kind"], *counts)
	}
	stop.allow = true
	stop.denyLater = false
	claim := a4Claim(t, b)
	adoption := &b2CommitFault{Backend: b, mode: "not_committed"}
	o.backend = adoption
	failed := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", claim.ID))))
	if failed["kind"] != "failed" || !adoption.tripped || counts.proof != 1 {
		t.Fatal("prior adoption stage not reached")
	}
	adoption.allow = true
	success := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", claim.ID))))
	if success["kind"] != "recovered" || counts.proof != 1 {
		t.Fatal("known noncommit republished incorrectly", success["kind"])
	}
	o.backend = b
	o.settlementAuthorization = func(context.Context, ReadSettlementDescriptor) error { return nil }
	status, e := o.SettleReadComposition(context.Background(), ch, rawJSON(t, q))
	if e != nil || status.State != "settled" {
		t.Fatal("staged graph settlement", status, e)
	}
	key := "alice\x00" + text(q, "requestId")
	s := loadHandoffSnapshot(t, b)
	roles := map[string]bool{}
	for _, d := range s.Journal[key].Composition.Settlement.Dependencies {
		roles[d.Role] = true
	}
	for _, role := range []string{"claim_prior", "candidate_acceptance", "candidate_delivery", "candidate_retirement"} {
		if !roles[role] {
			raw, _ := json.Marshal(roles)
			t.Fatal("role fixture missing", role, string(raw))
		}
	}
	return o, b, ch, q, counts, key
}
func TestB3aCandidateAndPriorDependencyRoles(t *testing.T) {
	for _, role := range []string{"claim_prior", "candidate_acceptance", "candidate_delivery", "candidate_retirement"} {
		for _, mode := range []string{"omit", "duplicate", "role", "substitute", "graph-foreign"} {
			t.Run(role+"/"+mode, func(t *testing.T) {
				o, b, _, _, counts, key := stagedSettlementGraph(t)
				g, _ := b.Acquire(context.Background())
				s, _ := b.Load(context.Background(), g)
				expected := s.Expected()
				s.Provenance["candidate-later"] = "synthetic"
				b.Commit(context.Background(), g, expected, s, "candidate-later-metadata")
				g.Close()
				before := loadHandoffSnapshot(t, b)
				target := ""
				for _, d := range before.Journal[key].Composition.Settlement.Dependencies {
					if d.Role == role {
						target = d.ID
						break
					}
				}
				if target == "" {
					t.Fatal("target absent")
				}
				fault := &dependencyFault{Backend: b, key: key, target: target, mode: mode}
				fresh, c2 := freshSettlement(t, o, fault)
				fresh.candidateBindings = o.candidateBindings
				prior := *counts
				request := compositionRequest(fresh)
				request["expectedGeneration"] = before.Generation
				got := resultMap(t)(fresh.Handle(context.Background(), c2, rawJSON(t, request)))
				if fault.hits == 0 || got["kind"] != "failed" || *counts != prior || !sameSnapshot(before, loadHandoffSnapshot(t, b)) {
					t.Fatal("candidate/prior dependency escaped", role, mode)
				}
			})
		}
	}
}
