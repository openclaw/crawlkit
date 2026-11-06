//go:build darwin

package sessionowner

import (
	"context"
	"testing"
	"time"
)

type multiCounts struct {
	reads, acquire, proof, requests int
	stages                          map[string]int
}

func multiCompositionFixture(t *testing.T, mode string) (*Owner, *MemoryBackend, *Channel, *multiCounts) {
	o, b, ch, _, _ := compositionFixture(t, "recover")
	counts := &multiCounts{stages: map[string]int{}}
	key := operationKey("fixture.read", 1, "perform")
	op := cloneOperation(o.registry.operations[key])
	op.BudgetPlans[0].MaxProviderRequests = 8
	op.BudgetPlans[0].MaxProviderBytes = 512
	if mode == "requests+1" {
		op.BudgetPlans[0].MaxProviderRequests = 7
	}
	if mode == "bytes+1" {
		op.BudgetPlans[0].MaxProviderBytes = 511
	}
	o.registry.operations[key] = op
	o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		stage := i.Step()
		switch stage {
		case "execute":
			counts.reads++
			stage = "initial"
			if counts.reads == 2 {
				stage = "retry"
			}
		case "acquire_candidate":
			counts.acquire++
		case "prove_candidate":
			counts.proof++
		}
		checkFrame := func() {
			session, e := NewAdapterSession(i, "multi-request-parent")
			if e != nil {
				t.Fatal(e)
			}
			frame, e := session.InvokeFrame()
			if e != nil {
				t.Fatal(e)
			}
			v, e := DecodeJSON(frame, HardFrameLimit)
			if e != nil {
				t.Fatal(e)
			}
			m, _ := object(v)
			budget, _ := object(m["budget"])
			used, reserved, e := compositionUsage(i.snapshot, i.compositionKey)
			if e != nil {
				t.Fatal(e)
			}
			root := i.snapshot.Journal[i.compositionKey].Composition
			nativeRemaining := i.plan.MaxProviderRequests - i.effects
			want := root.Plan.MaxProviderRequests - used
			if nativeRemaining < want {
				want = nativeRemaining
			}
			if int(num(budget["remainingProviderRequests"])) != want || int64(num(budget["remainingProviderBytes"])) != root.Plan.MaxProviderBytes-reserved {
				t.Fatal("private remaining budget drift", stage, budget)
			}
		}
		checkFrame()
		beforeRequests, beforeBytes := i.remainingProviderBudget()
		for n := 0; n < 2; n++ {
			requestCtx, stop := i.RequestContext(context.Background())
			if d, _ := requestCtx.Deadline(); d.After(i.Deadline()) {
				t.Fatal("request timeout escaped phase")
			}
			stop()
		}
		afterRequests, afterBytes := i.remainingProviderBudget()
		if beforeRequests != afterRequests || beforeBytes != afterBytes {
			t.Fatal("time-only RequestContext charged an effect")
		}
		for n := 0; n < 2; n++ {
			if e := i.BeforeEffect(ctx, map[string]string{"execute": "EXECUTING", "acquire_candidate": "ACQUIRING", "prove_candidate": "PROVING"}[i.Step()]); e != nil {
				return AdapterOutput{}, e
			}
			requestCtx, stop := i.RequestContext(ctx)
			if requestCtx.Err() != nil {
				stop()
				return AdapterOutput{}, requestCtx.Err()
			}
			counts.requests++
			counts.stages[stage]++
			body := make([]byte, 64)
			if mode == "body+1" {
				body = make([]byte, 65)
			}
			e := i.AcceptProviderBody(body)
			stop()
			if e != nil {
				return AdapterOutput{}, e
			}
			checkFrame()
		}
		switch i.Step() {
		case "execute":
			if counts.reads == 1 {
				return AdapterOutput{}, fail(Code("needs_login"), "multi-auth")
			}
			return AdapterOutput{Result: map[string]any{"value": "multi-public"}}, nil
		case "acquire_candidate":
			return AdapterOutput{Candidate: map[string]any{"token": "multi-candidate"}, CandidateExpires: o.now().Add(time.Hour)}, nil
		case "prove_candidate":
			return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
		}
		return AdapterOutput{}, fail(ProtocolMismatch, "multi-stage")
	}}
	return o, b, ch, counts
}
func TestB2B2AMultipleRequestsAndBodies(t *testing.T) {
	for _, mode := range []string{"exact", "requests+1", "bytes+1", "body+1"} {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, counts := multiCompositionFixture(t, mode)
			q := compositionRequest(o)
			got := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
			s := loadHandoffSnapshot(t, b)
			used, reserved, e := compositionUsage(s, "alice\x00"+text(q, "requestId"))
			if e != nil || used != counts.requests || reserved != int64(counts.requests*64) {
				t.Fatal("shared reservations drift")
			}
			if mode == "exact" {
				if got["kind"] != "performed" || counts.requests != 8 || counts.reads != 2 || counts.acquire != 1 || counts.proof != 1 {
					t.Fatal("exact multi-stage path")
				}
				for _, stage := range []string{"initial", "acquire_candidate", "prove_candidate", "retry"} {
					if counts.stages[stage] != 2 {
						t.Fatal("missing actual stage requests", stage)
					}
				}
			} else {
				want := 7
				if mode == "body+1" {
					want = 1
				}
				if got["kind"] != "failed" || counts.requests != want || counts.reads > 2 {
					t.Fatal("cap overflow dispatched", mode, got["kind"], *counts)
				}
			}
		})
	}
}
func TestB2B2AMultiRequestReopenNoRefund(t *testing.T) {
	for _, stored := range []bool{false, true} {
		name := "not-committed"
		if stored {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			o, memory, ch, counts := multiCompositionFixture(t, "exact")
			file, dir := fileFixture(t)
			initial := loadHandoffSnapshot(t, memory)
			g, _ := file.Acquire(context.Background())
			empty, _ := file.Load(context.Background(), g)
			if _, e := file.Commit(context.Background(), g, empty.Expected(), initial, "multi-enrollment"); e != nil {
				t.Fatal(e)
			}
			g.Close()
			fault := &composedOutcomeFault{Backend: file, target: "retry-intent", stored: stored, denyLater: true}
			o.backend = fault
			q := compositionRequest(o)
			got := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
			if got["kind"] != "failed" || !fault.hit || fault.laterFailures == 0 || counts.requests != 6 {
				t.Fatal("multi uncertainty stage missing", *counts)
			}
			key := "alice\x00" + text(q, "requestId")
			before := loadHandoffSnapshot(t, file)
			u, b, e := compositionUsage(before, key)
			if e != nil || !before.Journal[key].Composition.RetryReserved {
				t.Fatal("retry reservation missing")
			}
			freshFile, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
			if e != nil {
				t.Fatal(e)
			}
			fault.Backend = freshFile
			fault.denyLater = false
			fresh, c2 := reopenComposition(t, o, fault)
			prior := counts.requests
			got = resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
			if got["kind"] != "failed" || counts.requests != prior || fault.readbacks < 2 {
				t.Fatal("unknown effect replay")
			}
			fault.allow = true
			fresh, c2 = reopenComposition(t, o, fault)
			got = resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
			u2, b2, e := compositionUsage(loadHandoffSnapshot(t, freshFile), key)
			if e != nil || got["kind"] != "failed" || u2 != u || b2 != b || counts.requests != prior {
				t.Fatal("reopen refunded or replayed")
			}
		})
	}
}
