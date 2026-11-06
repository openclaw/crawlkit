package sessionowner

// Regression provenance: retained review_focused_test.go SHA256
// b21f7bd695da655e25ee635c3ff01798d0a415c40c532423de4bb2710a9d6e90.
// That red baseline remains immutable outside this checkout. These tests assert
// corrected behavior, not successful reproduction of the old defects.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type b1ReceiptBackend struct {
	injected bool
	Backend
	mode                string
	commits, reconciles int
	lastExpected        CommitReceipt
	old                 *Snapshot
	staleRead           bool
	deadlineObserved    bool
}

func (b *b1ReceiptBackend) Commit(ctx context.Context, g *Guard, e Expected, n *Snapshot, id string) (CommitReceipt, error) {
	b.commits++
	current, loadErr := b.Backend.Load(ctx, g)
	if loadErr != nil {
		return CommitReceipt{}, loadErr
	}
	if b.injected || a4TransactionStage(current, n, id) != "intent" {
		return b.Backend.Commit(ctx, g, e, n, id)
	}
	b.injected = true
	b.old, _ = b.Backend.Load(ctx, g)
	_, expected, err := prepareCommit(b.old, n, e, id)
	if err != nil {
		return CommitReceipt{}, err
	}
	b.lastExpected = expected
	switch b.mode {
	case "uncertain-nil", "reconcile-wrong", "reconcile-uncertain", "reconcile-block":
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	case "not-committed-nil":
		return CommitReceipt{OperationID: id, Certainty: "not_committed"}, nil
	case "empty-nil":
		return CommitReceipt{}, nil
	case "wrong-id":
		expected.OperationID = "wrong-id"
		return expected, nil
	case "wrong-generation":
		expected.Generation = "wrong-epoch"
		return expected, nil
	case "wrong-revision":
		expected.StateRevision--
		return expected, nil
	case "wrong-digest":
		expected.Digest = "wrong-digest"
		return expected, nil
	case "forged-positive":
		return expected, nil
	case "stale-readback":
		r, e := b.Backend.Commit(ctx, g, e, n, id)
		b.staleRead = true
		return r, e
	case "lost-ack-positive-readback":
		r, e := b.Backend.Commit(ctx, g, e, n, id)
		if e != nil {
			return r, e
		}
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	return CommitReceipt{}, errors.New("synthetic rejected commit")
}
func (b *b1ReceiptBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	if b.staleRead {
		b.staleRead = false
		return b.old.Clone()
	}
	return b.Backend.Load(ctx, g)
}
func (b *b1ReceiptBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	b.reconciles++
	switch b.mode {
	case "reconcile-wrong":
		r := b.lastExpected
		r.OperationID = "other"
		return r, nil
	case "reconcile-uncertain":
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	case "reconcile-block":
		_, b.deadlineObserved = ctx.Deadline()
		<-ctx.Done()
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, ctx.Err()
	}
	return b.Backend.Reconcile(ctx, g, id)
}

func TestB1R1MatchingDurableReceiptBeforeEffect(t *testing.T) {
	modes := []string{"uncertain-nil", "not-committed-nil", "empty-nil", "wrong-id", "wrong-generation", "wrong-revision", "wrong-digest", "forged-positive", "stale-readback", "reconcile-wrong", "reconcile-uncertain", "reconcile-block", "lost-ack-positive-readback"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			var effects atomic.Int32
			o, backend, c := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if err := i.BeforeEffect(ctx, "EXECUTING"); err != nil {
					return AdapterOutput{}, err
				}
				s, err := i.owner.backend.Load(ctx, i.guard)
				if err != nil {
					return AdapterOutput{}, err
				}
				if s.Journal[i.journalKey].Certainty != "uncertain" {
					t.Fatal("effect without persisted intent")
				}
				effects.Add(1)
				return AdapterOutput{Result: map[string]any{"value": "public"}}, nil
			})
			b := &b1ReceiptBackend{Backend: backend, mode: mode}
			o.backend = b
			start := time.Now()
			got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))))
			if !b.injected {
				t.Fatal("intended actual intent failure stage not exercised")
			}
			want := int32(0)
			if mode == "lost-ack-positive-readback" {
				want = 1
			}
			if effects.Load() != want || (got["kind"] == "performed") != (want == 1) {
				t.Fatalf("effect=%d outcome=%v", effects.Load(), got["kind"])
			}
			if mode == "reconcile-block" && (!b.deadlineObserved || time.Since(start) > time.Second) {
				t.Fatal("unbounded reconciliation")
			}
		})
	}
}

func TestB1R1NoPrivateACKForUnconfirmedCommit(t *testing.T) {
	var ackIssued bool
	o, backend, c := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		session, err := NewAdapterSession(i, "lifetime")
		if err != nil {
			return AdapterOutput{}, err
		}
		if _, err = session.InvokeFrame(); err != nil {
			return AdapterOutput{}, err
		}
		cp := map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": session.lease, "sequence": "0", "phase": "EXECUTING", "event": "before_effect", "effectCertainty": "not_dispatched", "evidenceId": "evidence", "evidence": map[string]any{"stage": "synthetic-before-effect"}}
		ack, _, err := session.Accept(ctx, rawJSON(t, cp))
		ackIssued = len(ack) > 0
		return AdapterOutput{}, err
	})
	o.backend = &b1ReceiptBackend{Backend: backend, mode: "uncertain-nil"}
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))))
	if ackIssued || got["kind"] == "performed" {
		t.Fatal("ACK without commit evidence")
	}
}

type b1GateAuth struct {
	fixtureAuth
	stage            string
	entered, proceed chan struct{}
}

func (a b1GateAuth) wait(stage string) {
	if a.stage == stage {
		close(a.entered)
		<-a.proceed
	}
}
func (a b1GateAuth) Allow(ctx context.Context, s string, op Operation, r map[string]any) error {
	a.wait("allow")
	return a.fixtureAuth.Allow(ctx, s, op, r)
}
func (a b1GateAuth) Confirm(ctx context.Context, s, ref string, op Operation, r map[string]any) error {
	a.wait("confirm")
	return a.fixtureAuth.Confirm(ctx, s, ref, op, r)
}
func (a b1GateAuth) Recovery(ctx context.Context, s, ref string, r *Request) error {
	a.wait("recovery")
	return a.fixtureAuth.Recovery(ctx, s, ref, r)
}

func TestB1R2CloseDuringAuthorization(t *testing.T) {
	for _, stage := range []string{"allow", "confirm", "recovery"} {
		t.Run(stage, func(t *testing.T) {
			var effects atomic.Int32
			o, _, c := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if err := i.BeforeEffect(ctx, "EXECUTING"); err != nil {
					return AdapterOutput{}, err
				}
				effects.Add(1)
				return AdapterOutput{Result: map[string]any{"value": "public"}}, nil
			})
			a := b1GateAuth{stage: stage, entered: make(chan struct{}), proceed: make(chan struct{})}
			o.auth = a
			q := fixtureRequest("perform")
			if stage == "confirm" {
				q["operationId"] = "fixture.write"
				q["confirmationRef"] = "approval-1"
			}
			if stage == "recovery" {
				q = fixtureRequest("recover")
				q["mode"] = "new"
				q["expectedGeneration"] = "epoch-1"
				q["cause"] = "authorized_replacement"
				q["rejectedRequestId"] = nil
				q["recoveryAuthorizationRef"] = "policy-1"
			}
			type result struct {
				b []byte
				e error
			}
			done := make(chan result, 1)
			go func() { b, e := o.Handle(context.Background(), c, rawJSON(t, q)); done <- result{b, e} }()
			select {
			case <-a.entered:
			case <-time.After(time.Second):
				t.Fatal("authorization not reached")
			}
			c.Close()
			close(a.proceed)
			select {
			case r := <-done:
				if r.e == nil {
					got := resultMap(t)(r.b, r.e)
					if got["kind"] == "performed" || got["kind"] == "recovered" {
						t.Fatal("closed channel admitted")
					}
				}
				if effects.Load() != 0 {
					t.Fatal("closed channel effect")
				}
			case <-time.After(time.Second):
				t.Fatal("close race hung")
			}
		})
	}
}

type b1CloseAtCommit struct {
	intentHit bool
	Backend
	commits int
	close   func()
}

func (b *b1CloseAtCommit) Commit(ctx context.Context, g *Guard, e Expected, n *Snapshot, id string) (CommitReceipt, error) {
	b.commits++
	current, err := b.Backend.Load(ctx, g)
	if err != nil {
		return CommitReceipt{}, err
	}
	if !b.intentHit && a4TransactionStage(current, n, id) == "intent" {
		b.intentHit = true
		b.close()
	}
	return b.Backend.Commit(ctx, g, e, n, id)
}
func TestB1R2CloseInsideCheckpointCallback(t *testing.T) {
	var effects int
	o, b, c := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if err := i.BeforeEffect(ctx, "EXECUTING"); err != nil {
			return AdapterOutput{}, err
		}
		effects++
		return AdapterOutput{Result: map[string]any{"value": "public"}}, nil
	})
	fault := &b1CloseAtCommit{Backend: b, close: c.Close}
	o.backend = fault
	done := make(chan struct{})
	go func() { o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))); close(done) }()
	select {
	case <-done:
		if !fault.intentHit {
			t.Fatal("close did not target actual intent")
		}
		if effects != 0 {
			t.Fatal("effect after close")
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle lock held across backend callback")
	}
}

func b1RestartOwner(t *testing.T, old *Owner, b Backend, exceptions []OperationRef) (*Owner, *Channel) {
	t.Helper()
	o, e := NewOwner(OwnerOptions{Registry: old.registry, Backend: b, Authorizer: fixtureAuth{}, Adapters: old.adapters, UncertainReadOperations: exceptions})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(old, o)
	c, e := o.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	return o, c
}
func TestB1R3DurableBindingFenceAndQualifiedRead(t *testing.T) {
	var effects int
	o, b, c := fixtureStateOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if err := i.BeforeEffect(ctx, "EXECUTING"); err != nil {
			return AdapterOutput{}, err
		}
		effects++
		if i.Operation().Semantics == "mutation" {
			return AdapterOutput{}, errors.New("synthetic lost response")
		}
		return AdapterOutput{Result: map[string]any{"value": "public"}}, nil
	})
	write := func() map[string]any {
		q := fixtureRequest("perform")
		q["operationId"] = "fixture.write"
		q["confirmationRef"] = "approval-1"
		return q
	}
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, write())))
	if first["certainty"] != "uncertain" {
		t.Fatal("missing uncertainty")
	}
	o, c = b1RestartOwner(t, o, b, nil)
	status := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("inspect"))))
	if status["state"] != "uncertain" || status["nextAction"] != "reconcile" {
		t.Fatal("false ready")
	}
	for _, q := range []map[string]any{write(), fixtureRequest("perform")} {
		got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
		if got["kind"] != "failed" || effects != 1 {
			t.Fatal("unqualified work bypassed durable fence")
		}
	}
	o, c = b1RestartOwner(t, o, b, []OperationRef{{ID: "fixture.read", Version: 1}})
	read := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))))
	if read["kind"] != "performed" || effects != 2 {
		failure, _ := object(read["failure"])
		t.Fatalf("qualified read unavailable: kind=%v category=%v certainty=%v effects=%d", read["kind"], failure["category"], read["certainty"], effects)
	}
	status = resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("inspect"))))
	if status["state"] != "uncertain" {
		t.Fatal("read cleared fence")
	}
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, write())))
	if got["kind"] != "failed" || effects != 2 {
		t.Fatal("mutation fence cleared")
	}
	if _, e := NewOwner(OwnerOptions{Registry: o.registry, Backend: b, Authorizer: fixtureAuth{}, UncertainReadOperations: []OperationRef{{ID: "fixture.write", Version: 1}}}); e == nil {
		t.Fatal("mutation exception")
	}
}

func TestB1R3RecoveringInspection(t *testing.T) {
	o, b, c := fixtureOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) {
		t.Fatal("inspect effect")
		return AdapterOutput{}, nil
	})
	b2Seed(t, o, b)
	status := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("inspect"))))
	if status["state"] != "recovering" || status["nextAction"] != "resume_claim" {
		t.Fatal("pending claim reported ready")
	}
}

func TestB1R4ActualPrivateAndRPCDeadlines(t *testing.T) { testA2ControlledDeadlineProperties(t) }

func TestB1R5R6SharedSchemaCorpus(t *testing.T) {
	raw, err := os.ReadFile("conformance/b1-schema-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string            `json:"name"`
		Schema   json.RawMessage   `json:"schema"`
		Register bool              `json:"register"`
		Valid    []json.RawMessage `json:"valid"`
		Invalid  []json.RawMessage `json:"invalid"`
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			store, err := NewSchemaStore()
			if err != nil {
				t.Fatal(err)
			}
			err = store.Register("b1-schema", c.Schema)
			if (err == nil) != c.Register {
				t.Fatalf("register expected %v, got %v", c.Register, err)
			}
			if !c.Register {
				return
			}
			for _, b := range c.Valid {
				v, e := DecodeJSON(b, HardFrameLimit)
				if e != nil {
					t.Fatal(e)
				}
				if e = store.Validate("b1-schema", v); e != nil {
					t.Fatal("valid fixture refused", e)
				}
			}
			for _, b := range c.Invalid {
				v, e := DecodeJSON(b, HardFrameLimit)
				if e != nil {
					t.Fatal(e)
				}
				if e = store.Validate("b1-schema", v); e == nil {
					t.Fatal("invalid fixture accepted")
				}
			}
		})
	}
}

func TestB1R4PrivateHostTimeout(t *testing.T) {
	var elapsed time.Duration
	o, _, c := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		session, e := NewAdapterSession(i, "private-timeout")
		if e != nil {
			return AdapterOutput{}, e
		}
		parent, child := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer child.Close()
			if _, e := ReadFrame(child, HardFrameLimit); e != nil {
				return
			}
			var b [1]byte
			_, _ = child.Read(b[:])
		}()
		start := time.Now()
		out, err := RunPrivateFD(ctx, session, parent)
		elapsed = time.Since(start)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("private peer not settled")
		}
		return out, err
	})
	key := operationKey("fixture.read", 1, "perform")
	op := cloneOperation(o.registry.operations[key])
	rpc := int64(10)
	op.BudgetPlans[0].MaxRPCMs = &rpc
	o.registry.operations[key] = op
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))))
	if got["kind"] == "performed" || elapsed > 200*time.Millisecond {
		t.Fatal("private host ignored RPC budget", elapsed)
	}
}
