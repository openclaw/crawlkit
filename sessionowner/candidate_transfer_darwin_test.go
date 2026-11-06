//go:build darwin

package sessionowner

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func transferRegistry(t *testing.T, max, frame int) *Registry {
	t.Helper()
	raw, e := os.ReadFile("conformance/registry.json")
	if e != nil {
		t.Fatal(e)
	}
	var policy map[string]any
	json.Unmarshal(raw, &policy)
	art := map[string][]byte{}
	for _, name := range []string{"target", "args", "result", "context", "candidate", "delta", "proof", "evidence"} {
		b, e := os.ReadFile("conformance/" + name + ".schema.json")
		if e != nil {
			t.Fatal(e)
		}
		if name == "candidate" {
			var v map[string]any
			json.Unmarshal(b, &v)
			v["properties"].(map[string]any)["token"].(map[string]any)["maxLength"] = max
			b, _ = json.Marshal(v)
		}
		art[name] = b
	}
	for _, v := range policy["schemas"].([]any) {
		m := v.(map[string]any)
		m["sha256"] = Hash(art[m["schemaId"].(string)])
	}
	for _, v := range policy["operations"].([]any) {
		m := v.(map[string]any)
		caps := m["wireCaps"].(map[string]any)
		caps["maxCandidateBytes"] = max
		caps["adapterFrameBytes"] = frame
	}
	b, _ := json.Marshal(policy)
	r, e := NewRegistry(b, Hash(b), art)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func transferOwner(t *testing.T, max, frame int) (*Owner, *MemoryBackend, *Channel, *ProtectedCandidateFiles) {
	t.Helper()
	o, b, c := fixtureRecoveryOwner(t, func(context.Context, *Invocation) (AdapterOutput, error) { return AdapterOutput{}, nil })
	o.registry = transferRegistry(t, max, frame)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	files, e := NewProtectedCandidateFiles(dir, "fixture-binding")
	if e != nil {
		t.Fatal(e)
	}
	o.candidateBindings = map[string]CandidateBinding{"fixture": {Repository: files, Expiry: ProtectedProducerExpiry}}
	return o, b, c, files
}
func privateResult(i *Invocation, lease any, candidate, proof any) map[string]any {
	return map[string]any{"protocol": AdapterProtocol, "kind": "adapter_result", "direction": "adapter_to_authority", "lease": lease, "sequence": "1", "status": "ok", "certainty": "completed", "result": nil, "failure": nil, "credentialDelta": nil, "candidate": candidate, "proof": proof}
}
func serveCandidatePeer(t *testing.T, ctx context.Context, i *Invocation, s *AdapterSession, files *ProtectedCandidateFiles, payload []byte, expiry time.Time, mode string) (AdapterOutput, error) {
	t.Helper()
	scope, e := s.StagingScope("adapter_to_authority")
	if e != nil {
		return AdapterOutput{}, e
	}
	parent, child := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer child.Close()
		raw, e := ReadFrame(child, i.Operation().WireCaps.AdapterFrameBytes)
		if e != nil {
			return
		}
		v, e := DecodeJSON(raw, HardFrameLimit)
		if e != nil {
			return
		}
		invoke, _ := object(v)
		phase := "ACQUIRING"
		if i.Step() == "prove_candidate" {
			phase = "PROVING"
			wire, _ := object(invoke["candidate"])
			if len(payload) > i.Operation().WireCaps.AdapterFrameBytes && text(wire, "mode") != "staged" {
				t.Error("oversized proof candidate silently inlined")
				return
			}
			if text(wire, "mode") == "staged" {
				_, e := s.StagingScope("authority_to_adapter")
				if e != nil {
					t.Error(e)
					return
				}
				m, e := s.ConsumeStagedCandidate(ctx, text(wire, "candidateRef"), int(num(wire["bytes"])), text(wire, "privateDigest"))
				if e != nil || Hash(m.Raw) != text(wire, "privateDigest") {
					t.Error("outbound grant mismatch", e)
					return
				}
				if _, e = s.ConsumeStagedCandidate(ctx, text(wire, "candidateRef"), int(num(wire["bytes"])), text(wire, "privateDigest")); e == nil {
					t.Error("outbound grant replay")
					return
				}
				decoded, e := DecodeJSON(m.Raw, HardFrameLimit)
				if e != nil || !equalJSON(decoded, normalize(i.Candidate)) {
					t.Error("wrong proof candidate")
					return
				}
			}
		}
		cp := map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": invoke["lease"], "sequence": "0", "phase": phase, "event": "before_effect", "effectCertainty": "not_dispatched", "evidenceId": "candidate-effect", "evidence": map[string]any{"stage": "synthetic-before-effect"}}
		if WriteFrame(child, rawJSON(t, cp), HardFrameLimit) != nil {
			return
		}
		if _, e = ReadFrame(child, HardFrameLimit); e != nil {
			return
		}
		var wire any
		var proof any
		if i.Step() == "acquire_candidate" {
			if mode == "staged" {
				ref, e := files.Write(ctx, scope, payload, expiry, i.Operation().WireCaps.MaxCandidateBytes)
				if e != nil {
					t.Error(e)
					return
				}
				wire = map[string]any{"mode": "staged", "candidateRef": ref.Ref, "bytes": ref.Bytes, "privateDigest": ref.Digest}
			} else {
				v, e := DecodeJSON(payload, HardFrameLimit)
				if e != nil {
					t.Error(e)
					return
				}
				wire = map[string]any{"mode": "inline", "value": v}
			}
		} else {
			proof = map[string]any{"account": "expected"}
		}
		WriteFrame(child, rawJSON(t, privateResult(i, invoke["lease"], wire, proof)), HardFrameLimit)
	}()
	out, e := RunPrivateFD(ctx, s, parent)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("private peer not settled")
	}
	return out, e
}
func TestStagedCandidateProtectedFDAndLargeProofGrant(t *testing.T) {
	o, b, c, files := transferOwner(t, 8192, 4096)
	payload := rawJSON(t, map[string]any{"token": strings.Repeat("z", 6000)})
	expiry := o.now().Add(time.Hour)
	captures, proofs := 0, 0
	o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		s, e := NewAdapterSession(i, "producer-parent")
		if e != nil {
			return AdapterOutput{}, e
		}
		if i.Step() == "acquire_candidate" {
			captures++
		} else {
			proofs++
		}
		return serveCandidatePeer(t, ctx, i, s, files, payload, expiry, "staged")
	}}
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	if got["kind"] != "recovered" || captures != 1 || proofs != 1 {
		t.Fatalf("staged flow kind=%v capture=%d proof=%d", got["kind"], captures, proofs)
	}
	encoded, _ := json.Marshal(got)
	if bytes.Contains(encoded, bytes.Repeat([]byte("z"), 32)) {
		t.Fatal("candidate leaked publicly")
	}
	g, _ := b.Acquire(context.Background())
	state, _ := b.Load(context.Background(), g)
	g.Close()
	if len(state.CandidateTransfers) != 2 {
		t.Fatal("incoming/outgoing grants not durably owned")
	}
}
func TestInlineFDNeedsExplicitExpiryQualification(t *testing.T) {
	for _, qualified := range []bool{false, true} {
		name := "missing"
		if qualified {
			name = "qualified"
		}
		t.Run(name, func(t *testing.T) {
			o, _, c, files := transferOwner(t, 2048, 8192)
			payload := rawJSON(t, map[string]any{"token": "inline-new"})
			expiry := o.now().Add(time.Hour)
			if qualified {
				o.candidateBindings["fixture"] = CandidateBinding{Repository: files, Expiry: func(ctx context.Context, scope CandidateScope, value map[string]any, material CandidateMaterial) (time.Time, error) {
					return expiry, nil
				}}
			}
			o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				s, e := NewAdapterSession(i, "inline-parent")
				if e != nil {
					return AdapterOutput{}, e
				}
				return serveCandidatePeer(t, ctx, i, s, files, payload, expiry, "inline")
			}}
			got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
			if (got["kind"] == "recovered") != qualified {
				t.Fatal("expiry qualification bypass or missing mapping", got["kind"])
			}
		})
	}
}
func testCandidateScope() CandidateScope {
	return CandidateScope{BindingID: "fixture-binding", Caller: "alice", RequestID: "request", OperationID: "operation", OperationVersion: 1, OperationDigest: strings.Repeat("a", 64), AdapterID: "fixture", AdapterArtifact: strings.Repeat("b", 64), ClaimID: "claim", Epoch: "epoch", LeaseID: "lease", InvocationID: "invoke", ParentLifetimeID: "parent", Step: "acquire_candidate", Direction: "adapter_to_authority", LeaseDeadline: "2099-01-01T00:00:00.000Z"}
}
func TestProtectedCandidateFileCustodyAndTransportCaps(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	files, e := NewProtectedCandidateFiles(dir, "fixture-binding")
	if e != nil {
		t.Fatal(e)
	}
	scope := testCandidateScope()
	raw := []byte(`{"token":"fixture"}`)
	ref, e := files.Write(context.Background(), scope, raw, time.Now().Add(time.Hour), len(raw))
	if e != nil {
		t.Fatal(e)
	}
	m, e := files.Read(context.Background(), scope, ref.Ref, len(raw))
	if e != nil || !bytes.Equal(m.Raw, raw) {
		t.Fatal("exact transport", e)
	}
	if _, e = files.Read(context.Background(), scope, ref.Ref, len(raw)-1); e == nil {
		t.Fatal("+1 cap")
	}
	wrong := scope
	wrong.LeaseID = "different"
	if _, e = files.Read(context.Background(), wrong, ref.Ref, len(raw)); e == nil {
		t.Fatal("scope mismatch")
	}
	for _, bad := range []string{"../escape", "/absolute", ref.Ref + "/x"} {
		if _, e = files.Read(context.Background(), scope, bad, len(raw)); e == nil {
			t.Fatal("path selector")
		}
	}
	path, _ := files.path(ref.Ref)
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = files.Read(context.Background(), scope, ref.Ref, len(raw)); e == nil {
		t.Fatal("mode")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(dir, "hardlink")
	if e = os.Link(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e = files.Read(context.Background(), scope, ref.Ref, len(raw)); e == nil {
		t.Fatal("hardlink")
	}
	os.Remove(link)
	symlinkRef := newID()
	symlinkPath, _ := files.path(symlinkRef)
	os.Symlink(path, symlinkPath)
	if _, e = files.Read(context.Background(), scope, symlinkRef, len(raw)); e == nil {
		t.Fatal("symlink")
	}
}

func TestProtectedCandidateFIFORefusedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	files, e := NewProtectedCandidateFiles(dir, "fixture-binding")
	if e != nil {
		t.Fatal(e)
	}
	ref := newID()
	path, _ := files.path(ref)
	if e = syscall.Mkfifo(path, 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	go func() { _, e := files.Read(ctx, testCandidateScope(), ref, 1024); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(250 * time.Millisecond):
		fd, e := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if e == nil {
			syscall.Close(fd)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("special-file open blocked")
	}
}

func stagedDirectResult(t *testing.T, ctx context.Context, i *Invocation, files *ProtectedCandidateFiles, payload []byte, expiry time.Time, variant string) (AdapterOutput, error) {
	t.Helper()
	session, e := NewAdapterSession(i, "direct-parent")
	if e != nil {
		return AdapterOutput{}, e
	}
	if _, e = session.InvokeFrame(); e != nil {
		return AdapterOutput{}, e
	}
	scope, e := session.StagingScope("adapter_to_authority")
	if e != nil {
		return AdapterOutput{}, e
	}
	phase := "ACQUIRING"
	if i.Step() == "reconcile" {
		phase = "RECONCILING"
	}
	cp := map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": session.lease, "sequence": "0", "phase": phase, "event": "before_effect", "effectCertainty": "not_dispatched", "evidenceId": "direct-effect", "evidence": map[string]any{"stage": "synthetic-before-effect"}}
	if _, _, e = session.Accept(ctx, rawJSON(t, cp)); e != nil {
		return AdapterOutput{}, e
	}
	publishScope := scope
	if variant == "wrong-lease" {
		publishScope.LeaseID = "other-lease"
	}
	if variant == "wrong-caller" {
		publishScope.Caller = "bob"
	}
	if variant == "wrong-claim" {
		publishScope.ClaimID = "other-claim"
	}
	if variant == "wrong-scope" {
		publishScope.AdapterArtifact = strings.Repeat("c", 64)
	}
	ref, e := files.Write(ctx, publishScope, payload, expiry, HardFrameLimit)
	if e != nil {
		return AdapterOutput{}, e
	}
	wire := map[string]any{"mode": "staged", "candidateRef": ref.Ref, "bytes": ref.Bytes, "privateDigest": ref.Digest}
	if variant == "wrong-hash" {
		wire["privateDigest"] = strings.Repeat("0", 64)
	}
	if variant == "wrong-bytes" {
		wire["bytes"] = ref.Bytes + 1
	}
	if variant == "parent-loss" {
		session.ParentLost()
	}
	if variant == "cancel" {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		ctx = cancelled
	}
	raw := rawJSON(t, privateResult(i, session.lease, wire, nil))
	var out *AdapterOutput
	if variant == "concurrent" {
		type answer struct {
			out *AdapterOutput
			err error
		}
		answers := make(chan answer, 2)
		for n := 0; n < 2; n++ {
			go func() { _, v, err := session.Accept(ctx, raw); answers <- answer{v, err} }()
		}
		success := 0
		for n := 0; n < 2; n++ {
			a := <-answers
			if a.err == nil {
				success++
				out = a.out
			} else {
				e = a.err
			}
		}
		if success != 1 {
			t.Fatal("concurrent candidate used more than once", success)
		}
		e = nil
	} else {
		_, out, e = session.Accept(ctx, raw)
	}
	if out == nil {
		return AdapterOutput{}, e
	}
	if variant == "replay" {
		again := privateResult(i, session.lease, wire, nil)
		again["sequence"] = "2"
		if _, _, replayErr := session.Accept(ctx, rawJSON(t, again)); replayErr == nil {
			t.Fatal("same session candidate replay")
		}
	}
	return *out, e
}
func TestStagedCandidateNegativeAdmission(t *testing.T) {
	for _, variant := range []string{"wrong-scope", "wrong-lease", "wrong-caller", "wrong-claim", "wrong-hash", "wrong-bytes", "expired", "malformed", "schema", "missing-resolver", "parent-loss", "cancel", "current-rejected-encoding"} {
		t.Run(variant, func(t *testing.T) {
			o, b, c, files := transferOwner(t, 2048, 8192)
			expiry := o.now().Add(time.Hour)
			payload := []byte(`{"token":"new-candidate"}`)
			if variant == "expired" {
				expiry = o.now().Add(-time.Second)
			}
			if variant == "malformed" {
				payload = []byte(`{"token":`)
			}
			if variant == "schema" {
				payload = []byte(`{"token":"x","unexpected":true}`)
			}
			if variant == "missing-resolver" {
				o.candidateBindings["fixture"] = CandidateBinding{Expiry: ProtectedProducerExpiry}
			}
			if variant == "current-rejected-encoding" {
				payload = []byte(" { \"token\" : \"synthetic-private-\\u0074oken\" } ")
				g, _ := b.Acquire(context.Background())
				s, _ := b.Load(context.Background(), g)
				expected := s.Expected()
				s.AuthBlocked = true
				b.Commit(context.Background(), g, expected, s, "reject-current")
				g.Close()
			}
			proofs := 0
			o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if i.Step() != "acquire_candidate" {
					proofs++
					return AdapterOutput{}, fail(ProtocolMismatch, "unexpected_proof")
				}
				return stagedDirectResult(t, ctx, i, files, payload, expiry, variant)
			}}
			got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
			if got["kind"] == "recovered" || proofs != 0 {
				t.Fatal("staged admission bypass")
			}
			g, _ := b.Acquire(context.Background())
			s, _ := b.Load(context.Background(), g)
			g.Close()
			if s.Generation != "epoch-1" || variant == "current-rejected-encoding" && !s.AuthBlocked {
				t.Fatal("rejection changed authority")
			}
		})
	}
}
func TestStagedCandidateOneUseAndSemanticFingerprint(t *testing.T) {
	o, b, c, files := transferOwner(t, 2048, 8192)
	expiry := o.now().Add(time.Hour)
	payload := []byte(" { \"token\" : \"new-\\u0074oken\" } ")
	o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			return stagedDirectResult(t, ctx, i, files, payload, expiry, "replay")
		}
		if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
			return AdapterOutput{}, e
		}
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	}}
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	if got["kind"] != "recovered" {
		t.Fatal("valid semantic candidate", got["kind"])
	}
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	for _, tr := range s.CandidateTransfers {
		if tr.TransportDigest != Hash(payload) || tr.SemanticDigest != payloadDigest(map[string]any{"token": "new-token"}) || tr.TransportDigest == tr.SemanticDigest {
			t.Fatal("transport and semantic digest conflated")
		}
	}
}

type transferFault struct {
	outbound bool
	Backend
	id             string
	stored, allow  bool
	acceptAttempts int
}

func (b *transferFault) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	for _, tr := range s.CandidateTransfers {
		if (tr.State == "accepted" && !b.outbound || tr.State == "issued" && b.outbound) && tr.AcceptanceCommitID == id {
			b.acceptAttempts++
			if b.id == "" {
				b.id = id
				if b.stored {
					if _, err := b.Backend.Commit(ctx, g, e, s, id); err != nil {
						return CommitReceipt{}, err
					}
				}
				return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
			}
		}
	}
	return b.Backend.Commit(ctx, g, e, s, id)
}
func (b *transferFault) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.id && !b.allow {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func TestStagedCandidateAcceptanceReadbackWithoutRecapture(t *testing.T) {
	for _, stored := range []bool{false, true} {
		name := "not-committed"
		if stored {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			o, b, c, files := transferOwner(t, 2048, 8192)
			fault := &transferFault{Backend: b, stored: stored}
			o.backend = fault
			expiry := o.now().Add(time.Hour)
			captures, proofs := 0, 0
			o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if i.Step() == "acquire_candidate" {
					captures++
					return stagedDirectResult(t, ctx, i, files, []byte(`{"token":"accepted-retained"}`), expiry, "")
				}
				proofs++
				if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
					return AdapterOutput{}, e
				}
				return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
			}}
			first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
			if first["certainty"] != "uncertain" || captures != 1 {
				t.Fatal("acceptance fault not exercised")
			}
			// A positive/nonpositive local readback must use retained exact material,
			// not the still-readable producer file or a fresh expiry qualification.
			paths, _ := filepath.Glob(filepath.Join(files.directory, "*.candidate"))
			for _, path := range paths {
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
			}
			cl := a4Claim(t, b)
			fresh, e := NewOwner(OwnerOptions{Registry: o.registry, Backend: fault, Authorizer: fixtureAuth{}, Adapters: o.adapters, Proof: o.proof, PrivateCandidates: o.candidateBindings})
			if e != nil {
				t.Fatal(e)
			}
			copyFixtureTime(o, fresh)
			ch, _ := fresh.AuthenticateChannel(context.Background(), "alice")
			blocked := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID))))
			if blocked["kind"] != "failed" || captures != 1 || proofs != 0 {
				t.Fatal("uncertain candidate acceptance replayed")
			}
			fault.allow = true
			got := resultMap(t)(fresh.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID))))
			if got["kind"] != "recovered" || captures != 1 || proofs != 1 {
				t.Fatal("retained acceptance readback", got["kind"], captures, proofs)
			}
		})
	}
}

func TestStagedCandidateBindingExactCapAndConcurrency(t *testing.T) {
	for _, variant := range []string{"exact", "over", "concurrent"} {
		t.Run(variant, func(t *testing.T) {
			max := 256
			o, _, c, files := transferOwner(t, max, 8192)
			n := 244
			if variant == "over" {
				n++
			}
			payload := rawJSON(t, map[string]any{"token": strings.Repeat("q", n)})
			if variant != "over" && len(payload) != max {
				t.Fatal("exact fixture length")
			}
			proofs := 0
			expiry := o.now().Add(time.Hour)
			o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if i.Step() == "acquire_candidate" {
					return stagedDirectResult(t, ctx, i, files, payload, expiry, variant)
				}
				proofs++
				if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
					return AdapterOutput{}, e
				}
				return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
			}}
			got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
			if variant == "over" {
				if got["kind"] == "recovered" || proofs != 0 {
					t.Fatal("binding max+1 admitted")
				}
			} else if got["kind"] != "recovered" || proofs != 1 {
				t.Fatal("exact or concurrent candidate failed", got["kind"])
			}
		})
	}
}

func TestStagedCandidateProtectedBackendReopenRetainsExpiryAndAttempts(t *testing.T) {
	template, memory, _, files := transferOwner(t, 2048, 8192)
	file, dir := fileFixture(t)
	gm, _ := memory.Acquire(context.Background())
	initial, _ := memory.Load(context.Background(), gm)
	gm.Close()
	gf, _ := file.Acquire(context.Background())
	empty, _ := file.Load(context.Background(), gf)
	if _, e := file.Commit(context.Background(), gf, empty.Expected(), initial, "stage-seed"); e != nil {
		t.Fatal(e)
	}
	gf.Close()
	fault := &transferFault{Backend: file}
	template.backend = fault
	expiry := template.now().Add(time.Hour)
	captures, proofs := 0, 0
	template.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			captures++
			return stagedDirectResult(t, ctx, i, files, []byte(`{"token":"disk-candidate"}`), expiry, "")
		}
		proofs++
		if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
			return AdapterOutput{}, e
		}
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	}}
	channel, _ := template.AuthenticateChannel(context.Background(), "alice")
	first := resultMap(t)(template.Handle(context.Background(), channel, rawJSON(t, b2Request("new", ""))))
	if first["certainty"] != "uncertain" || fault.id == "" {
		t.Fatal("disk boundary not hit")
	}
	cl := a4Claim(t, file)
	fresh, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		t.Fatal(e)
	}
	owner, e := NewOwner(OwnerOptions{Registry: template.registry, Backend: fresh, Authorizer: fixtureAuth{}, Adapters: template.adapters, Proof: template.proof, PrivateCandidates: template.candidateBindings})
	if e != nil {
		t.Fatal(e)
	}
	copyFixtureTime(template, owner)
	ch, _ := owner.AuthenticateChannel(context.Background(), "alice")
	got := resultMap(t)(owner.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", cl.ID))))
	if got["kind"] != "recovered" || captures != 1 || proofs != 1 {
		t.Fatal("disk resume recapture or failure", got["kind"], captures, proofs)
	}
	retained := a4Claim(t, fresh)
	if retained.AcquisitionAttempts != 1 || retained.ProofAttempts != 1 || !retained.CandidateExpires.Equal(expiry) {
		t.Fatal("counts or expiry changed")
	}
}

func TestStagedCandidateReconciliationHandoff(t *testing.T) {
	o, b, c, files := transferOwner(t, 2048, 8192)
	captures, proofs, lookups := 0, 0, 0
	expiry := o.now().Add(time.Hour)
	execute := fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if i.Step() == "acquire_candidate" {
			captures++
			if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{}, fail(ReconciliationRequired, "lost_candidate")
		}
		proofs++
		if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
			return AdapterOutput{}, e
		}
		return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
	}}
	o.adapters["fixture"] = b2Reconciler{execute, func(ctx context.Context, i *Invocation, r RecoveryReconcileRequest) (RecoveryReconcileResult, error) {
		lookups++
		out, e := stagedDirectResult(t, ctx, i, files, []byte(`{"token":"reconciled-stage"}`), expiry, "")
		if e != nil {
			if safe, ok := e.(*Error); ok {
				t.Log("reconcile private refusal", safe.Code, safe.Detail)
			} else {
				t.Log("reconcile error type", ErrorCode(e))
			}
		}
		return RecoveryReconcileResult{Request: r, Status: "candidate_ready", EvidenceID: "stage-readback", Candidate: out.Candidate, CandidateExpires: out.CandidateExpires, CandidateTransferID: out.CandidateTransferID}, e
	}}
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
	id := b2ClaimID(t, first)
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", id))))
	if got["kind"] != "recovered" || captures != 1 || proofs != 1 || lookups != 1 {
		t.Fatal("reconciliation staged handoff", got["kind"], captures, proofs, lookups)
	}
	if a4Claim(t, b).CandidateTransferKey == "" {
		t.Fatal("reconciled transfer not bound")
	}
}
func TestStagedCandidateOutboundUncertainPublication(t *testing.T) {
	for _, stored := range []bool{false, true} {
		name := "not-committed"
		if stored {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			o, b, c, files := transferOwner(t, 8192, 4096)
			fault := &transferFault{Backend: b, stored: stored, outbound: true}
			o.backend = fault
			captures, proofCalls := 0, 0
			expiry := o.now().Add(time.Hour)
			value := map[string]any{"token": strings.Repeat("r", 6000)}
			payload := rawJSON(t, value)
			o.adapters["fixture"] = fixtureAdapter{func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if i.Step() == "acquire_candidate" {
					captures++
					if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
						return AdapterOutput{}, e
					}
					return AdapterOutput{Candidate: value, CandidateExpires: expiry}, nil
				}
				proofCalls++
				s, e := NewAdapterSession(i, "outbound-parent")
				if e != nil {
					return AdapterOutput{}, e
				}
				return serveCandidatePeer(t, ctx, i, s, files, payload, expiry, "staged")
			}}
			first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("new", ""))))
			if first["kind"] == "recovered" || fault.id == "" {
				t.Fatal("outbound fault not hit")
			}
			id := a4Claim(t, b).ID
			blocked := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", id))))
			if blocked["kind"] != "failed" || proofCalls != 1 {
				t.Fatal("unknown outbound grant replayed")
			}
			fault.allow = true
			got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, b2Request("resume", id))))
			if got["kind"] != "recovered" || captures != 1 || proofCalls != 2 {
				t.Fatal("outbound exact readback", got["kind"], captures, proofCalls)
			}
			cl := a4Claim(t, b)
			if cl.AcquisitionAttempts != 1 || cl.ProofAttempts != 1 || !cl.CandidateExpires.Equal(expiry) {
				t.Fatal("outbound counts or expiry")
			}
		})
	}
}
