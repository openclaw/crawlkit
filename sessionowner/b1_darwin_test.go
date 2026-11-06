//go:build darwin

package sessionowner

import (
	"context"
	"errors"
	"testing"
)

func TestB1R3DiskRestartKeepsUncertaintyFence(t *testing.T) {
	var effects int
	template, _, _ := fixtureStateOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
			return AdapterOutput{}, e
		}
		effects++
		return AdapterOutput{}, errors.New("synthetic lost mutation result")
	})
	b, dir := fileFixture(t)
	g, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	s, e := b.Load(context.Background(), g)
	if e != nil {
		t.Fatal(e)
	}
	expected := s.Expected()
	s.Generation = "epoch-1"
	s.Credentials = map[string]any{"token": "synthetic-private-token"}
	s.Identity = map[string]any{"account": "expected"}
	if _, e = b.Commit(context.Background(), g, expected, s, "seed"); e != nil {
		t.Fatal(e)
	}
	g.Close()
	o, c := b1RestartOwner(t, template, b, nil)
	q := fixtureRequest("perform")
	q["operationId"] = "fixture.write"
	q["confirmationRef"] = "approval-1"
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
	if first["certainty"] != "uncertain" || effects != 1 {
		t.Fatal("missing persisted uncertainty")
	}
	fresh, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		t.Fatal(e)
	}
	o, c = b1RestartOwner(t, template, fresh, nil)
	status := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("inspect"))))
	if status["state"] != "uncertain" {
		t.Fatal("disk restart lost fence")
	}
	q = fixtureRequest("perform")
	q["operationId"] = "fixture.write"
	q["confirmationRef"] = "approval-1"
	q["target"] = map[string]any{"id": "different-target"}
	next := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
	if next["kind"] != "failed" || effects != 1 {
		t.Fatal("new target bypassed binding fence")
	}
}
