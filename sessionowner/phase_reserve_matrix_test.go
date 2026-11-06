package sessionowner

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func b3bSyntheticPlan() BudgetPlan {
	return BudgetPlan{ID: "b3b-owner-plan", Readiness: "none", PhaseMaxMs: map[string]int64{"queue": 7, "import": 11, "startup": 13, "capture": 17, "proof": 19, "action": 250, "commit": 3, "cleanup": 5, "ipc": 7}, MaxTotalMs: 10000, MaxProviderRequests: 8, MaxProviderBytes: 33554432, MaxSafeReadRetries: 1}
}

func TestB2C3BPhaseReserve(t *testing.T) {
	phases := []string{"queue", "import", "startup", "capture", "proof", "action", "commit", "cleanup", "ipc"}
	for index, phase := range phases {
		t.Run(fmt.Sprintf("B%02d_reserve_%s", 15+index, phase), func(t *testing.T) {
			now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
			plan := b3bSyntheticPlan()
			before := payloadDigest(plan)
			remaining := []string{phase}
			if phase != "cleanup" {
				remaining = append(remaining, "cleanup")
			}
			// Independent arithmetic oracle: unique phases, action reserve1ms and cleanup
			// only once. Do not call phaseReserve to compute its own expected answer.
			required := int64(0)
			seen := map[string]bool{}
			for _, name := range remaining {
				if seen[name] {
					t.Fatal("fixture duplicated phase")
				}
				seen[name] = true
				cost := plan.PhaseMaxMs[name]
				if cost <= 0 {
					t.Fatal("fixture phase cost not positive")
				}
				if name == "action" {
					cost = 1
				}
				required += cost
			}
			if !seen["cleanup"] {
				required += plan.PhaseMaxMs["cleanup"]
			}
			req := &Request{Deadline: now.Add(time.Duration(required) * time.Millisecond), Operation: Operation{BudgetPlans: []BudgetPlan{plan}}}
			got, e := ReservePlan(req, now, false, remaining)
			if e != nil || payloadDigest(got) != before {
				t.Fatal("exact independent reserve refused", phase, required, e)
			}
			req.Deadline = now.Add(time.Duration(required-1) * time.Millisecond)
			_, e = ReservePlan(req, now, false, remaining)
			if ErrorCode(e) != BudgetInsufficient {
				t.Fatal("one millisecond short did not fail budget", phase, e)
			}
			typed, ok := e.(*Error)
			if !ok || typed.Detail != "phase_reserve" {
				t.Fatal("negative hit wrong reserve stage", e)
			}
			if payloadDigest(plan) != before || payloadDigest(req.Operation.BudgetPlans[0]) != before {
				t.Fatal("reserve mutated owner plan")
			}
			if phase == "cleanup" {
				if len(remaining) != 1 || remaining[0] != "cleanup" || required != plan.PhaseMaxMs["cleanup"] {
					t.Fatal("cleanup doublecount fixture")
				}
				req.Deadline = now.Add(time.Duration(required) * time.Millisecond)
				if _, e := ReservePlan(req, now, false, nil); e != nil {
					t.Fatal("absent cleanup not added exactly once", e)
				}
			}
			if phase == "startup" {
				req.Deadline = now.Add(time.Duration(plan.PhaseMaxMs["cleanup"]) * time.Millisecond)
				if _, e := ReservePlan(req, now, true, remaining); e != nil {
					t.Fatal("qualified warm startup not skipped", e)
				}
				if _, e := ReservePlan(req, now, false, remaining); ErrorCode(e) != BudgetInsufficient {
					t.Fatal("cold startup incorrectly skipped", e)
				}
			}
			t.Logf("B3B_RESERVE phase=%s unique=%v exactMs=%d shortMs=%d actionCeilingMs=%d planUnchanged=true", phase, remaining, required, required-1, plan.PhaseMaxMs["action"])
		})
	}
	for index, item := range []struct{ phase, step string }{{"startup", "readiness"}, {"capture", "acquire_candidate"}, {"proof", "prove_candidate"}, {"action", "execute"}} {
		t.Run(fmt.Sprintf("B%02d_runPhase_%s", 24+index, item.phase), func(t *testing.T) {
			adapterCalls := 0
			o, base, backend, _, _ := b3bFixture(t, "fixture-binding", "fixture-reader", "phase-private-context", func(context.Context, *Invocation) (AdapterOutput, error) { adapterCalls++; return AdapterOutput{}, nil })
			clock := &a2Clock{now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
			o.now, o.deadlineHook = clock.Now, clock.Until
			now := clock.Now()
			q := fixtureRequest("perform")
			if item.step != "execute" {
				q = fixtureRequest("recover")
				q["mode"] = "new"
				q["expectedGeneration"] = "epoch-1"
				q["cause"] = "authorized_replacement"
				q["rejectedRequestId"] = nil
				q["recoveryAuthorizationRef"] = "policy-1"
			}
			req, e := o.registry.ParseRequest(rawJSON(t, q))
			if e != nil {
				t.Fatal("registered synthetic invocation request invalid", e)
			}
			plan := b3bSyntheticPlan()
			plan.MaxSafeReadRetries = 0
			plan.MaxAcquisitionAttempts = 1
			plan.MaxProofAttempts = 1
			plan.MaxReadinessResumptions = 2
			planBefore := payloadDigest(plan)
			policyBefore := payloadDigest(o.registry.policy)
			tail := time.Duration(plan.PhaseMaxMs["commit"]+plan.PhaseMaxMs["cleanup"]+plan.PhaseMaxMs["ipc"]) * time.Millisecond
			req.Deadline = now.Add(tail + 200*time.Millisecond)
			req.Value["deadline"] = timestamp(req.Deadline)
			ctx, stop := o.contextUntil(context.Background(), now.Add(tail+100*time.Millisecond))
			defer stop()
			snapshot := loadHandoffSnapshot(t, base)
			inv := &Invocation{owner: o, request: req, plan: plan, snapshot: snapshot, operationCtx: ctx, outerDeadline: req.Deadline, step: item.step, Context: cloneMap(snapshot.Credentials), Target: mapValue(req.Value["target"]), Args: mapValue(req.Value["args"])}
			expected := now.Add(100 * time.Millisecond)
			if ceiling := now.Add(time.Duration(plan.PhaseMaxMs[item.phase]) * time.Millisecond); ceiling.Before(expected) {
				expected = ceiling
			}
			callbacks := 0
			var observed context.Context
			_, e = o.runPhase(ctx, inv, func(phase context.Context, _ *Invocation) (AdapterOutput, error) {
				callbacks++
				observed = phase
				deadline, ok := phase.Deadline()
				if !ok || !deadline.Equal(expected) || phase.Err() != nil {
					t.Error("positive actual phase context not clamped", item.phase, deadline, expected)
				}
				return AdapterOutput{}, nil
			})
			if e != nil || callbacks != 1 || observed == nil || observed.Err() == nil {
				t.Fatal("positive phase callback/context cleanup", item.phase, e, callbacks)
			}
			negative := *req
			negative.Value = cloneMap(req.Value)
			negative.Deadline = now.Add(tail)
			negative.Value["deadline"] = timestamp(negative.Deadline)
			negativeCtx, negativeStop := o.contextUntil(context.Background(), negative.Deadline)
			defer negativeStop()
			negativeInv := &Invocation{owner: o, request: &negative, plan: plan, snapshot: snapshot, operationCtx: negativeCtx, outerDeadline: negative.Deadline, step: item.step, Context: cloneMap(snapshot.Credentials), Target: mapValue(req.Value["target"]), Args: mapValue(req.Value["args"])}
			negativeCallbacks := 0
			_, e = o.runPhase(negativeCtx, negativeInv, func(context.Context, *Invocation) (AdapterOutput, error) {
				negativeCallbacks++
				return AdapterOutput{}, nil
			})
			typed, ok := e.(*Error)
			if !ok || typed.Code != BudgetInsufficient || typed.Detail != "phase_deadline" || negativeCallbacks != 0 || adapterCalls != 0 || backend.counts() != [4]int32{} || !sameSnapshot(snapshot, loadHandoffSnapshot(t, base)) {
				t.Fatal("tail insufficiency reached callback/BeforeEffect/storage/provider", item.phase, e, negativeCallbacks, backend.counts())
			}
			if payloadDigest(plan) != planBefore || payloadDigest(inv.plan) != planBefore || payloadDigest(o.registry.policy) != policyBefore {
				t.Fatal("phase test changed registered policy or plan")
			}
			t.Logf("B3B_PHASE phase=%s positiveCallbacks=1 negativeCallbacks=0 tailMs=%d remainingNegativeMs=%d adapterCalls=0 storageCalls=0", item.phase, tail/time.Millisecond, negative.Deadline.Sub(now)/time.Millisecond)
		})
	}
}
