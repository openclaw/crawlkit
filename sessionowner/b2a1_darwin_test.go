//go:build darwin

package sessionowner

import (
	"context"
	"testing"
)

func TestB2A1PrivateFileReloadRejectsUnknownPhase(t *testing.T) {
	file, dir := fileFixture(t)
	g, e := file.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	old, _ := file.Load(context.Background(), g)
	s := b2a1State("RESERVED")
	s.StateRevision = old.StateRevision
	s.Claims["state-claim"].Phase = "PRIVATE_UNKNOWN_PHASE"
	if _, e = file.Commit(context.Background(), g, old.Expected(), s, "persist-invalid"); e != nil {
		t.Fatal(e)
	}
	g.Close()
	fresh, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		t.Fatal(e)
	}
	template, _, _ := fixtureOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) {
		t.Fatal("file reload dispatched")
		return AdapterOutput{}, nil
	})
	o, c := b1RestartOwner(t, template, fresh, nil)
	status := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("inspect"))))
	if status["state"] != "uncertain" {
		t.Fatal("unknown persisted phase became ready")
	}
	q := fixtureRequest("perform")
	q["operationId"] = "fixture.write"
	q["confirmationRef"] = "approval-1"
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
	if got["kind"] != "failed" {
		t.Fatal("unknown file phase escaped")
	}
}
