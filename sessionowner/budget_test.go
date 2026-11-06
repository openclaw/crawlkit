package sessionowner

import (
	"testing"
	"time"
)

func TestElapsedEntryAndRemainingPhaseBudget(t *testing.T) {
	now := time.Now()
	phase := map[string]int64{"queue": 1000, "import": 0, "startup": 0, "capture": 0, "proof": 0, "action": 240000, "commit": 1000, "cleanup": 1000, "ipc": 1000}
	req := &Request{Deadline: now.Add(240000*time.Millisecond - time.Millisecond), Operation: Operation{BudgetPlans: []BudgetPlan{{ID: "native-ceiling", Readiness: "none", PhaseMaxMs: phase, MaxTotalMs: 240000}}}}
	if _, err := ReservePlan(req, now, false, []string{"queue", "action", "commit", "cleanup", "ipc"}); err != nil {
		t.Fatal("one millisecond elapsed must not reserve full action ceiling", err)
	}
	phase = map[string]int64{"queue": 1000, "import": 0, "startup": 120000, "capture": 20000, "proof": 75000, "action": 0, "commit": 1000, "cleanup": 5000, "ipc": 1000}
	req.Operation.BudgetPlans = []BudgetPlan{{ID: "warm-proof", Readiness: "warm_only", PhaseMaxMs: phase, MaxTotalMs: 120000}}
	req.Deadline = now.Add(100000 * time.Millisecond)
	if _, err := ReservePlan(req, now, true, []string{"proof", "commit", "cleanup", "ipc"}); err != nil {
		t.Fatal("completed20s capture must not reserve again", err)
	}
	if _, err := ReservePlan(req, now, false, []string{"startup", "capture", "proof", "commit", "cleanup", "ipc"}); err == nil {
		t.Fatal("warm became cold")
	}
	r := fixtureRegistry(t)
	wire := fixtureRequest("perform")
	wire["phase"] = "proof"
	if _, err := r.ParseRequest(rawJSON(t, wire)); err == nil {
		t.Fatal("public phase selector")
	}
}
