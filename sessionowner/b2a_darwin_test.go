//go:build darwin

package sessionowner

import (
	"context"
	"testing"
)

func TestB2ARetainedCandidatePrivateFileRestart(t *testing.T) {
	var proofs int
	template, memory, _ := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() != "prove_candidate" {
			t.Fatal("restart reacquired candidate")
		}
		proofs++
		if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
			return AdapterOutput{}, e
		}
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	})
	file, dir := fileFixture(t)
	gm, _ := memory.Acquire(context.Background())
	initial, _ := memory.Load(context.Background(), gm)
	gm.Close()
	gf, _ := file.Acquire(context.Background())
	empty, _ := file.Load(context.Background(), gf)
	if _, e := file.Commit(context.Background(), gf, empty.Expected(), initial, "seed-enrollment"); e != nil {
		t.Fatal(e)
	}
	gf.Close()
	cl := b2Seed(t, template, file)
	fresh, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		t.Fatal(e)
	}
	o, e := NewOwner(OwnerOptions{Registry: template.registry, Backend: fresh, Authorizer: fixtureAuth{}, Adapters: template.adapters, Proof: template.proof})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(template, o)
	c, _ := o.AuthenticateChannel(context.Background(), "alice")
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", cl.ID))))
	if got["kind"] != "recovered" || proofs != 1 {
		t.Fatalf("restart result %v", got["kind"])
	}
}
