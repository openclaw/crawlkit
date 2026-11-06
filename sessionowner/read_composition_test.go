package sessionowner

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type compositionCounts struct {
	read, acquire, proof, authorize int
	seenTarget, seenArgs            string
}

func compositionFixture(t *testing.T, mode string) (*Owner, *MemoryBackend, *Channel, *a2Clock, *compositionCounts) {
	t.Helper()
	o, b, _ := fixtureRecoveryOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) { return AdapterOutput{}, nil })
	clock := &a2Clock{now: o.now()}
	o.now = clock.Now
	o.deadlineHook = clock.Until
	for key, op := range o.registry.operations {
		op = cloneOperation(op)
		op.WireCaps.MaxProviderBodyBytes = 64
		if op.RequestKind == "perform" && op.Semantics == "safe_read" {
			for n := range op.BudgetPlans {
				p := &op.BudgetPlans[n]
				p.MaxProviderRequests = 4
				p.MaxProviderBytes = 256
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
	counts := &compositionCounts{}
	o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if e := i.BeforeEffect(ctx, map[string]string{"execute": "EXECUTING", "acquire_candidate": "ACQUIRING", "prove_candidate": "PROVING"}[i.Step()]); e != nil {
			return AdapterOutput{}, e
		}
		if e := i.AcceptProviderBody([]byte("body")); e != nil {
			return AdapterOutput{}, e
		}
		switch i.Step() {
		case "execute":
			counts.read++
			target, args := payloadDigest(i.Target), payloadDigest(i.Args)
			if counts.read == 1 {
				counts.seenTarget = target
				counts.seenArgs = args
			} else if counts.seenTarget != target || counts.seenArgs != args {
				t.Error("retry changed arguments")
			}
			if mode == "initial-ok" || counts.read == 2 && mode != "second-auth" {
				return AdapterOutput{Result: map[string]any{"value": "public-result"}}, nil
			}
			if mode == "during-initial" {
				clock.Advance(i.Deadline())
			}
			if mode == "before-recovery" {
				clock.Advance(clock.Now().Add(4500 * time.Millisecond))
			}
			if mode == "throttled" {
				return AdapterOutput{}, fail(Code("throttled"), "fixture")
			}
			if mode == "malformed" {
				return AdapterOutput{Result: map[string]any{"unexpected": true}}, nil
			}
			return AdapterOutput{}, fail(Code("needs_login"), "fixture-classified-auth")
		case "acquire_candidate":
			counts.acquire++
			if mode == "during-recovery" {
				clock.Advance(i.Deadline())
			}
			return AdapterOutput{Candidate: map[string]any{"token": "replacement-secret"}, CandidateExpires: clock.Now().Add(time.Hour)}, nil
		case "prove_candidate":
			counts.proof++
			if mode == "wrong-account" {
				return AdapterOutput{Proof: map[string]any{"account": "other"}}, nil
			}
			return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
		}
		return AdapterOutput{}, errors.New("unexpected")
	}}
	authorization := func(ctx context.Context, caller string, req *Request) (string, error) {
		counts.authorize++
		if caller != "alice" {
			return "", errors.New("not-authorized")
		}
		if mode == "unauthorized" {
			return "bad-policy", nil
		}
		return "policy-1", nil
	}
	fresh, e := NewOwner(OwnerOptions{Registry: o.registry, Backend: b, Authorizer: fixtureAuth{}, Adapters: o.adapters, Proof: o.proof, ReadRecoveryOperations: []OperationRef{{ID: "fixture.read", Version: 1}}, ReadRecoveryAuthorization: authorization})
	if e != nil {
		t.Fatal(e)
	}
	fresh.now = clock.Now
	fresh.deadlineHook = clock.Until
	ch, e := fresh.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	return fresh, b, ch, clock, counts
}
func compositionRequest(o *Owner) map[string]any {
	q := fixtureRequest("perform")
	q["deadline"] = timestamp(o.now().Add(5 * time.Second))
	return q
}
func TestB2B2OwnerCompositionStages(t *testing.T) {
	for _, mode := range []string{"initial-ok", "recover", "second-auth", "throttled", "malformed", "unauthorized", "wrong-account", "during-initial", "before-recovery", "during-recovery"} {
		t.Run(mode, func(t *testing.T) {
			o, b, ch, _, counts := compositionFixture(t, mode)
			q := compositionRequest(o)
			result := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
			t.Log("stages", mode, result["kind"], counts.read, counts.acquire, counts.proof)
			if result["requestId"] != q["requestId"] {
				t.Fatal("original correlation lost")
			}
			switch mode {
			case "initial-ok":
				if result["kind"] != "performed" || counts.read != 1 || counts.authorize != 0 || counts.acquire != 0 {
					t.Fatal("initial read did recovery")
				}
			case "recover":
				if result["kind"] != "performed" || counts.read != 2 || counts.acquire != 1 || counts.proof != 1 || result["acceptedGeneration"] != "epoch-1" || result["finalGeneration"] == "epoch-1" {
					t.Fatal("composition path incomplete", result)
				}
			case "second-auth":
				if result["kind"] != "failed" || counts.read != 2 || counts.acquire != 1 || counts.proof != 1 {
					t.Fatal("second auth was replayed")
				}
			default:
				if result["kind"] != "failed" || counts.read > 1 {
					t.Fatal("noneligible failure replay")
				}
			}
			g, _ := b.Acquire(context.Background())
			s, _ := b.Load(context.Background(), g)
			g.Close()
			if e := recoveryConsistency(s); e != nil {
				t.Fatal("invalid composed state", e)
			}
			root := s.Journal["alice\x00"+text(q, "requestId")]
			if root.Composition == nil {
				t.Fatal("composition not durable")
			}
			requests, reserved, e := compositionUsage(s, "alice\x00"+text(q, "requestId"))
			if e != nil || requests != counts.read+counts.acquire+counts.proof || reserved != int64(requests*64) || reserved > 256 {
				t.Fatal("monotonic usage", requests, reserved)
			}
			if mode == "initial-ok" || mode == "recover" || mode == "second-auth" {
				prior := *counts
				again := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
				if !equalJSON(result, again) || *counts != prior {
					t.Fatal("duplicate reexecuted")
				}
				fresh, e := NewOwner(OwnerOptions{Registry: o.registry, Backend: b, Authorizer: fixtureAuth{}, Adapters: o.adapters, Proof: o.proof, ReadRecoveryOperations: []OperationRef{{"fixture.read", 1}}, ReadRecoveryAuthorization: o.readRecoveryAuthorization})
				if e != nil {
					t.Fatal(e)
				}
				copyFixtureTime(o, fresh)
				c2, _ := fresh.AuthenticateChannel(context.Background(), "alice")
				q["deadline"] = timestamp(o.now().Add(time.Hour))
				again = resultMap(t)(fresh.HandleWithRecovery(context.Background(), c2, rawJSON(t, q)))
				if !equalJSON(result, again) || *counts != prior {
					t.Fatal("restart/fresh deadline reexecuted")
				}
			}
		})
	}
}
func TestB2B2ExplicitOptInAndBudgetCaps(t *testing.T) {
	for _, mode := range []string{"default", "zero-retry", "requests", "bytes", "native-body", "wrong-caller", "mutation", "caller-plan"} {
		t.Run(mode, func(t *testing.T) {
			o, _, ch, _, counts := compositionFixture(t, "recover")
			op := o.registry.operations[operationKey("fixture.read", 1, "perform")]
			switch mode {
			case "zero-retry":
				op.BudgetPlans[0].MaxSafeReadRetries = 0
			case "requests":
				op.BudgetPlans[0].MaxProviderRequests = 3
			case "bytes":
				op.BudgetPlans[0].MaxProviderBytes = 255
			case "native-body":
				op.WireCaps.MaxProviderBodyBytes = 3
			}
			o.registry.operations[operationKey("fixture.read", 1, "perform")] = op
			q := compositionRequest(o)
			if mode == "wrong-caller" {
				ch, _ = o.AuthenticateChannel(context.Background(), "bob")
			}
			if mode == "mutation" {
				q["operationId"] = "fixture.write"
				q["confirmationRef"] = "approval-1"
			}
			if mode == "caller-plan" {
				q["maxSafeReadRetries"] = 5
				_, e := o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q))
				if e == nil || counts.read != 0 {
					t.Fatal("caller plan admitted")
				}
				return
			}
			var raw []byte
			var e error
			if mode == "default" {
				raw, e = o.Handle(context.Background(), ch, rawJSON(t, q))
			} else {
				raw, e = o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q))
			}
			got := resultMap(t)(raw, e)
			if got["kind"] != "failed" || counts.acquire != 0 || counts.proof != 0 || counts.read > 1 {
				t.Fatal("scope/cap bypass", mode, got["kind"], *counts)
			}
		})
	}
}
func TestB2B2ConcurrentOriginalRequest(t *testing.T) {
	o, _, ch, _, counts := compositionFixture(t, "initial-ok")
	base := o.adapters["fixture"].(fixtureAdapter)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return base.Execute(ctx, i)
	}}
	q := rawJSON(t, compositionRequest(o))
	done := make(chan []byte, 1)
	go func() { b, _ := o.HandleWithRecovery(context.Background(), ch, q); done <- b }()
	<-entered
	busy := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, q))
	if busy["kind"] != "failed" {
		t.Fatal("concurrent request admitted")
	}
	close(release)
	raw := <-done
	var result map[string]any
	json.Unmarshal(raw, &result)
	if result["kind"] != "performed" || counts.read != 1 || calls.Load() != 1 {
		t.Fatal("concurrent replay")
	}
}
