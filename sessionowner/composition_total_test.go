package sessionowner

import (
	"context"
	"testing"
	"time"
)

type totalAuth struct {
	fixtureAuth
	hook func(context.Context, string)
}

func (a totalAuth) Allow(ctx context.Context, caller string, op Operation, v map[string]any) error {
	if op.RequestKind == "recover_new" || op.RequestKind == "recover_resume" {
		a.hook(ctx, "allow")
	}
	return a.fixtureAuth.Allow(ctx, caller, op, v)
}
func (a totalAuth) Recovery(ctx context.Context, caller, ref string, r *Request) error {
	a.hook(ctx, "recovery-auth")
	return a.fixtureAuth.Recovery(ctx, caller, ref, r)
}

type totalBackend struct {
	Backend
	hook    func(context.Context, string)
	adoptID string
}

func (b *totalBackend) Commit(ctx context.Context, g *Guard, e Expected, n *Snapshot, id string) (CommitReceipt, error) {
	old, err := b.Backend.Load(ctx, g)
	if err != nil {
		return CommitReceipt{}, err
	}
	for key, j := range n.Journal {
		if j.CompositionKey != "" && j.RequestKind == "recover" && old.Journal[key] == nil {
			b.hook(ctx, "prepare")
		}
	}
	for _, c := range n.Claims {
		if c.Phase == "CANDIDATE_READY" && c.Candidate != nil {
			b.hook(ctx, "inter-phase")
		}
	}
	if n.Generation != e.Generation {
		b.adoptID = id
		b.hook(ctx, "adoption")
	}
	r, err := b.Backend.Commit(ctx, g, e, n, id)
	if id == b.adoptID && err == nil {
		b.hook(ctx, "post-adoption")
	}
	return r, err
}
func (b *totalBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	r, e := b.Backend.Reconcile(ctx, g, id)
	if id == b.adoptID && id != "" {
		b.hook(ctx, "adoption-readback")
	}
	return r, e
}
func TestB2B2ANativeChildTotal(t *testing.T) {
	for _, mode := range []string{"positive", "reference", "allow", "recovery-auth", "prepare", "inter-phase", "adoption", "post-adoption", "adoption-readback", "reserve-after-auth"} {
		t.Run(mode, func(t *testing.T) {
			o, memory, ch, clock, counts := compositionFixture(t, "recover")
			for _, kind := range []string{"recover_new", "recover_resume"} {
				op := cloneOperation(o.registry.builtins[kind])
				op.BudgetPlans[0].MaxTotalMs = 1200
				o.registry.builtins[kind] = op
				o.registry.operations[operationKey(op.ID, op.Version, op.RequestKind)] = op
			}
			var admitted, deadline time.Time
			hit, phaseChecked := false, false
			hook := func(ctx context.Context, stage string) {
				if deadline.IsZero() {
					t.Fatal("child admission not recorded")
				}
				d, ok := ctx.Deadline()
				if !ok || d.After(deadline) {
					t.Fatal("child context exceeds native total", stage, d, deadline)
				}
				if hit {
					return
				}
				if mode == stage {
					hit = true
					clock.Advance(deadline)
				}
				if mode == "reserve-after-auth" && stage == "allow" {
					hit = true
					clock.Advance(admitted.Add(250 * time.Millisecond))
				}
			}
			o.auth = totalAuth{hook: hook}
			baseAuthorization := o.readRecoveryAuthorization
			o.readRecoveryAuthorization = func(ctx context.Context, caller string, r *Request) (string, error) {
				admitted = clock.Now()
				deadline = admitted.Add(1200 * time.Millisecond)
				hook(ctx, "reference")
				return baseAuthorization(ctx, caller, r)
			}
			backend := &totalBackend{Backend: memory, hook: hook}
			o.backend = backend
			base := o.adapters["fixture"].(fixtureAdapter)
			o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if i.Step() != "execute" {
					phaseChecked = true
					if !i.request.Deadline.Equal(deadline) || !i.outerDeadline.Equal(deadline) {
						t.Fatal("native total not carried to request/invocation")
					}
					if d, _ := i.operationCtx.Deadline(); !d.Equal(deadline) {
						t.Fatal("native total not carried to operation context")
					}
					rpc, cancel := i.RequestContext(context.Background())
					if d, _ := rpc.Deadline(); d.After(i.Deadline()) || d.After(deadline) {
						t.Fatal("fresh RequestContext extended child")
					}
					cancel()
					session, e := NewAdapterSession(i, "native-total-parent")
					if e != nil {
						t.Fatal(e)
					}
					lease, _ := time.Parse("2006-01-02T15:04:05.000Z", text(session.lease, "deadline"))
					if lease.After(deadline) || lease.After(i.Deadline()) {
						t.Fatal("private lease extended total")
					}
				}
				return base.Execute(ctx, i)
			}}
			q := compositionRequest(o)
			rootDeadline, _ := time.Parse("2006-01-02T15:04:05.000Z", text(q, "deadline"))
			got := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
			if mode == "positive" {
				if got["kind"] != "performed" || counts.read != 2 || counts.acquire != 1 || counts.proof != 1 || !phaseChecked {
					t.Fatal("within-bound positive failed", got["kind"], *counts)
				}
			} else {
				if !hit || got["kind"] != "failed" || counts.read != 1 || !clock.Now().Before(rootDeadline) {
					t.Fatal("short child did not refuse under live parent", mode, got["kind"], hit, *counts)
				}
				if mode == "reference" || mode == "allow" || mode == "recovery-auth" || mode == "prepare" || mode == "reserve-after-auth" {
					if counts.acquire != 0 || counts.proof != 0 {
						t.Fatal("late authorization/preparation dispatched")
					}
				}
				if mode == "inter-phase" && counts.proof != 0 {
					t.Fatal("expired child started proof")
				}
			}
		})
	}
}
