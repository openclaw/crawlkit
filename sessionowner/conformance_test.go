package sessionowner

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureRegistry(t *testing.T) *Registry {
	t.Helper()
	raw, e := os.ReadFile("conformance/registry.json")
	if e != nil {
		t.Fatal(e)
	}
	art := map[string][]byte{}
	for _, n := range []string{"target", "args", "result", "context", "candidate", "delta", "proof", "evidence"} {
		b, e := os.ReadFile("conformance/" + n + ".schema.json")
		if e != nil {
			t.Fatal(e)
		}
		art[n] = b
	}
	r, e := NewRegistry(raw, Hash(raw), art)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func fixtureRequest(kind string) map[string]any {
	m := map[string]any{"protocol": Protocol, "kind": kind, "requestId": newID(), "bindingId": "fixture-binding", "deadline": timestamp(time.Now().Add(20 * time.Second))}
	if kind == "perform" {
		m["expectedGeneration"] = "epoch-1"
		m["operationId"] = "fixture.read"
		m["operationVersion"] = 1
		m["target"] = map[string]any{"id": "target-1"}
		m["args"] = map[string]any{"cursor": ""}
		m["confirmationRef"] = nil
	}
	return m
}
func rawJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func resultMap(t *testing.T) func([]byte, error) map[string]any {
	t.Helper()
	return func(b []byte, e error) map[string]any {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
		v, e := DecodeJSON(b, HardFrameLimit)
		if e != nil {
			t.Fatal(e)
		}
		m, ok := object(v)
		if !ok {
			t.Fatal("not object")
		}
		return m
	}
}

func TestSharedCorpus(t *testing.T) {
	r := fixtureRegistry(t)
	b, e := os.ReadFile("conformance/corpus.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name    string `json:"name"`
		Plane   string `json:"plane"`
		Wire    string `json:"wire"`
		Hex     string `json:"wireHex"`
		Request string `json:"request"`
		Want    bool   `json:"want"`
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			raw := []byte(c.Wire)
			if c.Hex != "" {
				raw, e = hex.DecodeString(c.Hex)
				if e != nil {
					t.Fatal(e)
				}
			}
			switch c.Plane {
			case "json":
				_, e = DecodeJSON(raw, HardFrameLimit)
			case "request":
				_, e = r.ParseRequest(raw)
			case "response":
				req, er := r.ParseRequest([]byte(c.Request))
				if er != nil {
					t.Fatal(er)
				}
				e = r.ValidateResponse(raw, req)
			default:
				t.Fatal("unknown corpus plane")
			}
			if (e == nil) != c.Want {
				t.Fatalf("want valid=%v, got %v", c.Want, e)
			}
		})
	}
}
func TestFrameBoundaries(t *testing.T) {
	for _, n := range []int{8192, 16384, 87423, 8388608} {
		payload := bytes.Repeat([]byte("x"), n)
		var buf bytes.Buffer
		if e := WriteFrame(&buf, payload, n); e != nil {
			t.Fatal(e)
		}
		got, e := ReadFrame(&buf, n)
		if e != nil || !bytes.Equal(got, payload) {
			t.Fatalf("boundary %d", n)
		}
		if e = WriteFrame(io.Discard, append(payload, 'x'), n); e == nil {
			t.Fatalf("accepted +1 %d", n)
		}
	}
	if _, e := DecodeJSON([]byte(strings.Repeat("[", 65)+"0"+strings.Repeat("]", 65)), 4096); e == nil {
		t.Fatal("depth")
	}
}
func TestLargeRegisteredParity(t *testing.T) {
	r := fixtureRegistry(t)
	req := fixtureRequest("perform")
	req["args"] = map[string]any{"cursor": strings.Repeat("\x01", 87423)}
	raw := rawJSON(t, req)
	if _, e := r.ParseRequest(raw); e != nil {
		t.Fatal(e)
	}
	req["args"] = map[string]any{"cursor": strings.Repeat("x", 87424)}
	if _, e := r.ParseRequest(rawJSON(t, req)); e == nil {
		t.Fatal("cursor +1")
	}
	req = fixtureRequest("perform")
	parsed, e := r.ParseRequest(rawJSON(t, req))
	if e != nil {
		t.Fatal(e)
	}
	response := map[string]any{"protocol": Protocol, "kind": "performed", "requestId": req["requestId"], "bindingId": "fixture-binding", "acceptedGeneration": "epoch-1", "finalGeneration": "epoch-1", "stateRevision": "1", "receiptRef": "receipt-1", "certainty": "completed", "nextAction": "none", "retryNotBefore": nil, "operationId": "fixture.read", "operationVersion": 1, "result": map[string]any{"value": strings.Repeat("\x01", 8388608)}}
	b := rawJSON(t, response)
	if len(b) < 50331648 {
		t.Fatal("escaping fixture not exercised")
	}
	if e = r.ValidateResponse(b, parsed); e != nil {
		t.Fatal(e)
	}
	response["result"] = map[string]any{"value": strings.Repeat("x", 8388609)}
	if e = r.ValidateResponse(rawJSON(t, response), parsed); e == nil {
		t.Fatal("body +1")
	}
}

type fixtureAuth struct{}

func (fixtureAuth) Authenticate(ctx context.Context, peer any) (string, error) {
	s, ok := peer.(string)
	if !ok {
		return "", errors.New("denied")
	}
	return s, nil
}
func (fixtureAuth) Allow(ctx context.Context, subject string, op Operation, request map[string]any) error {
	if subject != "alice" && subject != "bob" {
		return errors.New("denied")
	}
	return nil
}
func (fixtureAuth) Confirm(ctx context.Context, subject, ref string, op Operation, request map[string]any) error {
	if ref != "approval-1" {
		return errors.New("denied")
	}
	return nil
}
func (fixtureAuth) Recovery(ctx context.Context, subject, ref string, req *Request) error {
	if ref != "policy-1" {
		return errors.New("denied")
	}
	return nil
}

type fixtureAdapter struct {
	fn func(context.Context, *Invocation) (AdapterOutput, error)
}

func (f fixtureAdapter) ArtifactID() string { return strings.Repeat("b", 64) }
func (f fixtureAdapter) Execute(ctx context.Context, i *Invocation) (AdapterOutput, error) {
	return f.fn(ctx, i)
}
func fixtureOwner(t *testing.T, fn func(context.Context, *Invocation) (AdapterOutput, error)) (*Owner, *MemoryBackend, *Channel) {
	t.Helper()
	s := NewSnapshot("fixture-binding")
	s.Generation = "epoch-1"
	s.Credentials = map[string]any{"token": "synthetic-private-token"}
	s.Identity = map[string]any{"account": "expected"}
	b, e := NewMemoryBackend(s)
	if e != nil {
		t.Fatal(e)
	}
	o, e := NewOwner(OwnerOptions{Registry: fixtureRegistry(t), Backend: b, Authorizer: fixtureAuth{}, Adapters: map[string]Adapter{"fixture": fixtureAdapter{fn}}, Proof: func(ctx context.Context, s *Snapshot, c, p map[string]any) error {
		if text(p, "account") != "expected" {
			return errors.New("wrong")
		}
		return nil
	}, Delta: func(ctx context.Context, s *Snapshot, d map[string]any) (map[string]any, error) { return d, nil }})
	if e != nil {
		t.Fatal(e)
	}
	c, e := o.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	return o, b, c
}

// fixtureRecoveryOwner freezes only owner-local deadline scheduling for tests
// about recovery state/candidate fences, not elapsed-time enforcement. Production
// default and all declared limits are unchanged; temporal tests use fixtureOwner.
func fixtureStateOwner(t *testing.T, fn func(context.Context, *Invocation) (AdapterOutput, error)) (*Owner, *MemoryBackend, *Channel) {
	o, b, c := fixtureOwner(t, fn)
	clock := &a2Clock{now: time.Now().UTC().Truncate(time.Millisecond)}
	o.now = clock.Now
	o.deadlineHook = clock.Until
	return o, b, c
}
func fixtureRecoveryOwner(t *testing.T, fn func(context.Context, *Invocation) (AdapterOutput, error)) (*Owner, *MemoryBackend, *Channel) {
	return fixtureStateOwner(t, fn)
}
func copyFixtureTime(from, to *Owner) { to.now = from.now; to.deadlineHook = from.deadlineHook }

func TestOwnerInspectAdmissionAndNoReplay(t *testing.T) {
	var calls atomic.Int32
	o, b, c := fixtureStateOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		calls.Add(1)
		if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
			return AdapterOutput{}, e
		}
		return AdapterOutput{Result: map[string]any{"value": "ordinary public output"}}, nil
	})
	q := fixtureRequest("inspect")
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
	if got["kind"] != "inspection" || calls.Load() != 0 {
		t.Fatal("inspect effects")
	}
	q = fixtureRequest("perform")
	q["operationId"] = "fixture.write"
	q["confirmationRef"] = "approval-1"
	raw := rawJSON(t, q)
	first, e := o.Handle(context.Background(), c, raw)
	if e != nil {
		t.Fatal(e)
	}
	second, e := o.Handle(context.Background(), c, raw)
	if e != nil || !bytes.Equal(first, second) || calls.Load() != 1 {
		t.Fatal("replay")
	}
	q["target"] = map[string]any{"id": "different"}
	got = resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
	if got["kind"] != "failed" || calls.Load() != 1 {
		t.Fatal("mismatched duplicate")
	}
	other, e := o.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	third, e := o.Handle(context.Background(), other, raw)
	if e != nil || !bytes.Equal(first, third) || calls.Load() != 1 {
		t.Fatal("cross-channel dedup")
	}
	g, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	s, e := b.Load(context.Background(), g)
	g.Close()
	if e != nil || len(s.Journal) != 1 {
		t.Fatal("journal")
	}
}
func TestCancellationSameAuthenticatedChannel(t *testing.T) {
	started := make(chan struct{})
	o, b, c := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
			return AdapterOutput{}, e
		}
		close(started)
		<-ctx.Done()
		return AdapterOutput{}, ctx.Err()
	})
	q := fixtureRequest("perform")
	q["operationId"] = "fixture.write"
	q["confirmationRef"] = "approval-1"
	done := make(chan map[string]any, 1)
	go func() { raw, e := o.Handle(context.Background(), c, rawJSON(t, q)); done <- resultMap(t)(raw, e) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch timeout")
	}
	cancel := fixtureRequest("cancel")
	cancel["targetRequestId"] = q["requestId"]
	cancel["reason"] = "caller_request"
	other, e := o.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	ack := resultMap(t)(o.Handle(context.Background(), other, rawJSON(t, cancel)))
	if ack["disposition"] != "not_found" {
		t.Fatal("cross channel")
	}
	ack = resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, cancel)))
	if ack["disposition"] != "cancellation_requested" || ack["certainty"] != "uncertain" {
		t.Fatal("ack certainty")
	}
	select {
	case got := <-done:
		if got["certainty"] != "uncertain" {
			t.Fatal("false certainty")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel timeout")
	}
	g, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	s, _ := b.Load(context.Background(), g)
	g.Close()
	for _, j := range s.Journal {
		if j.Dispatches != 1 || j.Certainty != "uncertain" {
			t.Fatal("journal lost")
		}
	}
}
func TestEpochRotationAndSecretFence(t *testing.T) {
	var n int
	o, b, c := fixtureStateOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		n++
		if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
			return AdapterOutput{}, e
		}
		if n == 1 {
			return AdapterOutput{Result: map[string]any{"value": "public"}, CredentialDelta: map[string]any{"token": "rotated-private-token"}}, nil
		}
		if i.Context["token"] != "rotated-private-token" {
			t.Fatal("stale snapshot")
		}
		return AdapterOutput{Result: map[string]any{"value": "rotated-private-token"}}, nil
	})
	first := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))))
	if first["finalGeneration"] != "epoch-1" {
		t.Fatal("rotation changed epoch")
	}
	second := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))))
	if second["kind"] != "failed" {
		t.Fatal("secret leaked")
	}
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	if s.CredentialRevision != 1 || s.Generation != "epoch-1" {
		t.Fatal("revisions")
	}
}
func TestRecoveryCandidateAndProof(t *testing.T) {
	var acquisition, proof int
	o, b, c := fixtureRecoveryOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		switch i.step {
		case "acquire_candidate":
			acquisition++
			if e := i.BeforeEffect(ctx, "ACQUIRING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{Candidate: map[string]any{"token": "new-private-token"}, CandidateExpires: time.Now().Add(time.Minute)}, nil
		case "prove_candidate":
			proof++
			if e := i.BeforeEffect(ctx, "PROVING"); e != nil {
				return AdapterOutput{}, e
			}
			return AdapterOutput{Proof: map[string]any{"account": "expected"}}, nil
		}
		return AdapterOutput{}, errors.New("unexpected")
	})
	q := fixtureRequest("recover")
	q["mode"] = "new"
	q["expectedGeneration"] = "epoch-1"
	q["cause"] = "authorized_replacement"
	q["rejectedRequestId"] = nil
	q["recoveryAuthorizationRef"] = "policy-1"
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
	if got["kind"] != "recovered" || got["finalGeneration"] == "epoch-1" || acquisition != 1 || proof != 1 {
		t.Fatalf("recovery result %v", got["kind"])
	}
	g, _ := b.Acquire(context.Background())
	s, _ := b.Load(context.Background(), g)
	g.Close()
	if s.AuthBlocked || s.Credentials["token"] != "new-private-token" || s.CredentialRevision != 1 {
		t.Fatal("adoption")
	}
}

func TestClaimsBudgetsCASAndSafeReadRetry(t *testing.T) {
	ctx := context.Background()
	s := NewSnapshot("fixture-binding")
	s.Generation = "epoch-1"
	b, _ := NewMemoryBackend(s)
	g, _ := b.Acquire(ctx)
	loaded, _ := b.Load(ctx, g)
	next, _ := loaded.Clone()
	next.AuthBlocked = true
	r, e := b.Commit(ctx, g, loaded.Expected(), next, "commit-1")
	if e != nil || r.Certainty != "committed" {
		t.Fatal(e)
	}
	if _, e = b.Commit(ctx, g, loaded.Expected(), next, "commit-2"); e == nil {
		t.Fatal("stale CAS")
	}
	known, e := b.Reconcile(ctx, g, "commit-1")
	if e != nil || known.Certainty != "committed" {
		t.Fatal("readback")
	}
	g.Close()
	p := BudgetPlan{MaxAcquisitionAttempts: 1, MaxReadinessResumptions: 2, MaxProofAttempts: 1}
	c := Claim{ID: "claim-1", Epoch: "epoch-1", Phase: "RESERVED"}
	now := time.Now()
	c, e = AdvanceClaim(c, "WAIT_READINESS", ClaimEvidence{ID: "proof-1", NoEffect: true}, p, now)
	if e != nil {
		t.Fatal(e)
	}
	c.RetryNotBefore = now.Add(time.Minute)
	if _, e = AdvanceClaim(c, "RESERVED", ClaimEvidence{ID: "proof-2", NoEffect: true}, p, now); e == nil {
		t.Fatal("cooldown")
	}
	c, e = AdvanceClaim(c, "RESERVED", ClaimEvidence{ID: "proof-2", NoEffect: true}, p, now.Add(2*time.Minute))
	if e != nil || c.ReadinessResumptions != 1 {
		t.Fatal("resume")
	}
	c, e = AdvanceClaim(c, "ACQUIRING", ClaimEvidence{ID: "proof-3"}, p, now.Add(2*time.Minute))
	if e != nil || c.AcquisitionAttempts != 0 {
		t.Fatal("reservation is not an effect attempt")
	}
	c, e = AdvanceClaim(c, "EFFECT_UNCERTAIN", ClaimEvidence{ID: "proof-4"}, p, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = AdvanceClaim(c, "RESERVED", ClaimEvidence{ID: "proof-5", NoEffect: true}, p, now); e == nil {
		t.Fatal("uncertain reset")
	}
	rg := fixtureRegistry(t)
	req, e := rg.ParseRequest(rawJSON(t, fixtureRequest("perform")))
	if e != nil {
		t.Fatal(e)
	}
	req.Deadline = now.Add(time.Millisecond)
	if _, e = ReservePlan(req, now, false, []string{"queue", "action", "cleanup"}); e == nil {
		t.Fatal("budget")
	}
	reads, recoveries := 0, 0
	op := Operation{Semantics: "safe_read"}
	p.MaxSafeReadRetries = 1
	e = SafeReadRetry(ctx, op, p, time.Now().Add(time.Second), time.Now, func(context.Context) error {
		reads++
		if reads == 1 {
			return fail("rejected_generation", "synthetic")
		}
		return nil
	}, func(context.Context) error { recoveries++; return nil })
	if e != nil || reads != 2 || recoveries != 1 {
		t.Fatal("safe retry")
	}
	reads = 0
	op.Semantics = "mutation"
	SafeReadRetry(ctx, op, p, time.Now().Add(time.Second), time.Now, func(context.Context) error { reads++; return fail("rejected_generation", "synthetic") }, func(context.Context) error { t.Fatal("mutation recovered"); return nil })
	if reads != 1 {
		t.Fatal("mutation replay")
	}
}

func TestPrivateFDCheckpointAndParentDeath(t *testing.T) {
	o, _, c := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		session, e := NewAdapterSession(i, "parent-lifetime")
		if e != nil {
			return AdapterOutput{}, e
		}
		parent, child := net.Pipe()
		go func() {
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
			lease := invoke["lease"]
			checkpoint := map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": lease, "sequence": "0", "phase": "EXECUTING", "event": "before_effect", "effectCertainty": "not_dispatched", "evidenceId": "evidence-1", "evidence": map[string]any{"stage": "synthetic-before-effect"}}
			if WriteFrame(child, rawJSON(t, checkpoint), HardFrameLimit) != nil {
				return
			}
			ack, e := ReadFrame(child, HardFrameLimit)
			if e != nil {
				return
			}
			av, e := DecodeJSON(ack, HardFrameLimit)
			if e != nil {
				return
			}
			am, _ := object(av)
			if am["authorizedToProceed"] != true {
				return
			}
			result := map[string]any{"protocol": AdapterProtocol, "kind": "adapter_result", "direction": "adapter_to_authority", "lease": lease, "sequence": "1", "status": "ok", "certainty": "completed", "result": map[string]any{"value": "public"}, "failure": nil, "credentialDelta": nil, "candidate": nil, "proof": nil}
			WriteFrame(child, rawJSON(t, result), HardFrameLimit)
		}()
		return RunPrivateFD(ctx, session, parent)
	})
	got := resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, fixtureRequest("perform"))))
	if got["kind"] != "performed" {
		t.Fatal("FD handshake")
	}
	o, _, c = fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
		session, e := NewAdapterSession(i, "parent-lifetime")
		if e != nil {
			return AdapterOutput{}, e
		}
		if _, e = session.InvokeFrame(); e != nil {
			return AdapterOutput{}, e
		}
		if e = i.BeforeEffect(ctx, "EXECUTING"); e != nil {
			return AdapterOutput{}, e
		}
		session.permitted.Store(true)
		if session.ParentLost() != "uncertain" {
			t.Fatal("parent death")
		}
		if _, e = session.InvokeFrame(); e == nil {
			t.Fatal("lease survived parent")
		}
		return AdapterOutput{}, fail(ReconciliationRequired, "synthetic-death")
	})
	q := fixtureRequest("perform")
	q["operationId"] = "fixture.write"
	q["confirmationRef"] = "approval-1"
	got = resultMap(t)(o.Handle(context.Background(), c, rawJSON(t, q)))
	if got["certainty"] != "uncertain" {
		t.Fatal("lost effect certainty")
	}
}

type fixturePSL struct{}

func (fixturePSL) PublicSuffix(domain string) string {
	if domain == "test" || strings.HasSuffix(domain, ".test") {
		return "test"
	}
	return domain
}
func (fixturePSL) String() string { return "synthetic .test only, NOT a production PSL" }
func TestCookiesCoherentSelectionAndRejection(t *testing.T) {
	p := CookiePolicy{PublicSuffixList: fixturePSL{}, MaxCookies: 20, MaxCookieBytes: 4096, Allow: func(*url.URL, *http.Cookie) error { return nil }, AllowTarget: func(u *url.URL) error {
		if u.Hostname() != "provider.test" {
			return errors.New("target")
		}
		return nil
	}, CSRF: func(c []*http.Cookie) (string, error) {
		value := ""
		for _, v := range c {
			if v.Name == "csrf" {
				if value != "" && value != v.Value {
					return "", errors.New("contradiction")
				}
				value = v.Value
			}
		}
		return value, nil
	}}
	s, e := NewCookieState(p, CookieSnapshot{})
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	if e = s.Apply("https://provider.test/a/b", []*http.Cookie{{Name: "auth", Value: "private", Secure: true, Path: "/"}, {Name: "csrf", Value: "one", Path: "/"}}, true, now); e != nil {
		t.Fatal(e)
	}
	selected, e := s.Select("https://provider.test/a", now)
	if e != nil || len(selected) != 2 {
		t.Fatal("selection")
	}
	selected, _ = s.Select("http://provider.test/a", now)
	if len(selected) != 1 {
		t.Fatal("secure")
	}
	old := s.Snapshot()
	if e = s.Apply("https://provider.test/a", []*http.Cookie{{Name: "csrf", Value: "two", Path: "/a"}}, true, now); e == nil || s.Snapshot().Revision != old.Revision {
		t.Fatal("csrf atomicity")
	}
	if e = s.Apply("https://provider.test/", []*http.Cookie{{Name: "auth", Value: "x", Domain: "test"}}, true, now); e == nil {
		t.Fatal("PSL")
	}
	if e = s.Apply("https://provider.test/", nil, false, now); e == nil {
		t.Fatal("public mutation")
	}
	if e = s.Apply("https://provider.test/", []*http.Cookie{{Name: "auth", Value: "", Path: "/", MaxAge: -1}}, true, now); e != nil {
		t.Fatal(e)
	}
	selected, _ = s.Select("https://provider.test/", now)
	if len(selected) != 1 {
		t.Fatal("deletion")
	}
	p.PublicSuffixList = nil
	if _, e = NewCookieState(p, CookieSnapshot{}); e == nil {
		t.Fatal("missing PSL")
	}
}

func TestFixturePathsNotLiveStores(t *testing.T) {
	entries, e := os.ReadDir("conformance")
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range entries {
		if filepath.Ext(p.Name()) == ".json" {
			b, e := os.ReadFile(filepath.Join("conformance", p.Name()))
			if e != nil {
				t.Fatal(e)
			}
			if bytes.Contains(b, []byte("/Users/")) {
				t.Fatal("machine path in fixture")
			}
		}
	}
}
