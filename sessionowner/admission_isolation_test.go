package sessionowner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const b3bPrivateError = "B3B_PRIVATE_AUTH_ERROR"

type b3bAuth struct {
	fixtureAuth
	authenticate, allow, confirm, recovery atomic.Int32
	deny                                   string
	mu                                     sync.Mutex
	lastCaller, lastOperation, lastTarget  string
}

func (a *b3bAuth) Authenticate(ctx context.Context, peer any) (string, error) {
	a.authenticate.Add(1)
	if a.deny == "authenticate" {
		return "", errors.New(b3bPrivateError)
	}
	return a.fixtureAuth.Authenticate(ctx, peer)
}
func (a *b3bAuth) Allow(ctx context.Context, caller string, op Operation, request map[string]any) error {
	a.allow.Add(1)
	a.mu.Lock()
	a.lastCaller, a.lastOperation, a.lastTarget = caller, op.ID, text(mapValue(request["target"]), "id")
	a.mu.Unlock()
	if a.deny == "allow" || a.deny == "target" && text(mapValue(request["target"]), "id") == "other-target" {
		return errors.New(b3bPrivateError)
	}
	return a.fixtureAuth.Allow(ctx, caller, op, request)
}
func (a *b3bAuth) Confirm(ctx context.Context, caller, ref string, op Operation, request map[string]any) error {
	a.confirm.Add(1)
	if a.deny == "confirm" {
		return errors.New(b3bPrivateError)
	}
	return a.fixtureAuth.Confirm(ctx, caller, ref, op, request)
}
func (a *b3bAuth) Recovery(ctx context.Context, caller, ref string, req *Request) error {
	a.recovery.Add(1)
	if a.deny == "recovery" {
		return errors.New(b3bPrivateError)
	}
	return a.fixtureAuth.Recovery(ctx, caller, ref, req)
}
func (a *b3bAuth) reset() {
	a.authenticate.Store(0)
	a.allow.Store(0)
	a.confirm.Store(0)
	a.recovery.Store(0)
}

type b3bObservedBackend struct {
	Backend
	acquires, loads, commits, reconciles atomic.Int32
}

func (b *b3bObservedBackend) Acquire(ctx context.Context) (*Guard, error) {
	b.acquires.Add(1)
	return b.Backend.Acquire(ctx)
}
func (b *b3bObservedBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	b.loads.Add(1)
	return b.Backend.Load(ctx, g)
}
func (b *b3bObservedBackend) Commit(ctx context.Context, g *Guard, x Expected, s *Snapshot, id string) (CommitReceipt, error) {
	b.commits.Add(1)
	return b.Backend.Commit(ctx, g, x, s, id)
}
func (b *b3bObservedBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	b.reconciles.Add(1)
	return b.Backend.Reconcile(ctx, g, id)
}
func (b *b3bObservedBackend) counts() [4]int32 {
	return [4]int32{b.acquires.Load(), b.loads.Load(), b.commits.Load(), b.reconciles.Load()}
}

func b3bRegistry(t *testing.T, binding, purpose string) *Registry {
	t.Helper()
	raw, e := os.ReadFile("conformance/registry.json")
	if e != nil {
		t.Fatal(e)
	}
	var policy map[string]any
	if e = json.Unmarshal(raw, &policy); e != nil {
		t.Fatal(e)
	}
	policy["bindingId"] = binding
	identity := policy["safeIdentity"].(map[string]any)
	identity["purposeAlias"] = purpose
	encoded, e := json.Marshal(policy)
	if e != nil {
		t.Fatal(e)
	}
	artifacts := map[string][]byte{}
	for _, name := range []string{"target", "args", "result", "context", "candidate", "delta", "proof", "evidence"} {
		b, e := os.ReadFile("conformance/" + name + ".schema.json")
		if e != nil {
			t.Fatal(e)
		}
		artifacts[name] = b
	}
	r, e := NewRegistry(encoded, Hash(encoded), artifacts)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func b3bFixture(t *testing.T, binding, purpose, token string, fn func(context.Context, *Invocation) (AdapterOutput, error)) (*Owner, *MemoryBackend, *b3bObservedBackend, *Channel, *b3bAuth) {
	t.Helper()
	r := b3bRegistry(t, binding, purpose)
	s := NewSnapshot(binding)
	s.Generation = "epoch-1"
	s.Credentials = map[string]any{"token": token}
	s.Identity = map[string]any{"account": "expected"}
	base, e := NewMemoryBackend(s)
	if e != nil {
		t.Fatal(e)
	}
	backend := &b3bObservedBackend{Backend: base}
	auth := &b3bAuth{}
	o, e := NewOwner(OwnerOptions{Registry: r, Backend: backend, Authorizer: auth, Adapters: map[string]Adapter{"fixture": fixtureAdapter{fn}}})
	if e != nil {
		t.Fatal(e)
	}
	clock := &a2Clock{now: time.Now().UTC().Truncate(time.Millisecond)}
	o.now, o.deadlineHook = clock.Now, clock.Until
	ch, e := o.AuthenticateChannel(context.Background(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	auth.reset()
	return o, base, backend, ch, auth
}
func b3bRequest(binding, id string) map[string]any {
	q := fixtureRequest("perform")
	q["bindingId"], q["requestId"] = binding, id
	return q
}
func b3bValid(t *testing.T, o *Owner, q map[string]any) []byte {
	t.Helper()
	raw := rawJSON(t, q)
	if _, e := o.registry.ParseRequest(raw); e != nil {
		t.Fatal("positive request shape invalid", e)
	}
	return raw
}
func b3bFailed(t *testing.T, raw []byte, e error, code Code) map[string]any {
	t.Helper()
	if e != nil {
		t.Fatal("expected correlated failed response", e)
	}
	m := resultMap(t)(raw, nil)
	f := mapValue(m["failure"])
	if m["kind"] != "failed" || text(f, "category") != string(code) || m["certainty"] != "not_dispatched" || strings.Contains(string(raw), b3bPrivateError) {
		t.Fatal("wrong safe pre-effect failure", m["kind"], f["category"], m["certainty"])
	}
	return m
}

type b3bCallResult struct {
	raw []byte
	err error
}
type b3bRunGroup struct {
	t        *testing.T
	wg       sync.WaitGroup
	cancels  []context.CancelFunc
	releases []func()
}

func b3bRuns(t *testing.T) *b3bRunGroup {
	g := &b3bRunGroup{t: t}
	t.Cleanup(func() {
		for _, cancel := range g.cancels {
			cancel()
		}
		for _, release := range g.releases {
			release()
		}
		done := make(chan struct{})
		go func() { g.wg.Wait(); close(done) }()
		select {
		case <-done:
			t.Log("B3B_CLEANUP all owned goroutines joined")
		case <-time.After(5 * time.Second):
			t.Error("owned fixture goroutines did not settle")
		}
	})
	return g
}
func (g *b3bRunGroup) start(o *Owner, ch *Channel, raw []byte) (<-chan b3bCallResult, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	g.cancels = append(g.cancels, cancel)
	done := make(chan b3bCallResult, 1)
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer cancel()
		out, e := o.Handle(ctx, ch, raw)
		done <- b3bCallResult{out, e}
	}()
	return done, cancel
}
func b3bAwait(t *testing.T, ch <-chan b3bCallResult) b3bCallResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("bounded operation result missing")
		return b3bCallResult{}
	}
}
func b3bSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded stage handshake missing")
	}
}

func TestB2C3BAdmissionIsolation(t *testing.T) {
	cases := []string{"B01_authenticate_denied", "B02_unenrolled_perform", "B03_allow_operation_denied", "B04_allow_target_scope_denied", "B05_confirm_denied", "B06_recover_authorization_denied", "B07_foreign_owner_channel", "B08_wrong_binding_request", "B09_cross_caller_same_id", "B10_independent_owner_purpose", "B28_public_cap_override"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if strings.HasPrefix(name, "B10") {
				group := b3bRuns(t)
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				group.releases = append(group.releases, unblock)
				var effectsA, effectsB atomic.Int32
				var held context.Context
				a, ab, _, ach, _ := b3bFixture(t, "binding-a", "purpose-a", "private-context-A", func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
					if text(i.Context, "token") != "private-context-A" {
						return AdapterOutput{}, errors.New("wrong A context")
					}
					if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
						return AdapterOutput{}, e
					}
					effectsA.Add(1)
					held = ctx
					close(entered)
					select {
					case <-release:
						return AdapterOutput{Result: map[string]any{"value": "owner-A-public"}}, nil
					case <-ctx.Done():
						return AdapterOutput{}, ctx.Err()
					}
				})
				b, bb, _, bch, _ := b3bFixture(t, "binding-b", "purpose-b", "private-context-B", func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
					if text(i.Context, "token") != "private-context-B" {
						return AdapterOutput{}, errors.New("wrong B context")
					}
					if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
						return AdapterOutput{}, e
					}
					effectsB.Add(1)
					return AdapterOutput{Result: map[string]any{"value": "owner-B-public"}}, nil
				})
				qa, qb := b3bRequest("binding-a", "shared-id"), b3bRequest("binding-b", "shared-id")
				aDone, _ := group.start(a, ach, b3bValid(t, a, qa))
				b3bSignal(t, entered)
				bDone, _ := group.start(b, bch, b3bValid(t, b, qb))
				br := b3bAwait(t, bDone)
				bm := resultMap(t)(br.raw, br.err)
				if bm["kind"] != "performed" || text(mapValue(bm["result"]), "value") != "owner-B-public" || held.Err() != nil || effectsB.Load() != 1 {
					t.Fatal("independent B blocked/crossed A")
				}
				select {
				case <-aDone:
					t.Fatal("A completed before its release")
				default:
				}
				unblock()
				ar := b3bAwait(t, aDone)
				am := resultMap(t)(ar.raw, ar.err)
				if am["kind"] != "performed" || text(mapValue(am["result"]), "value") != "owner-A-public" || effectsA.Load() != 1 {
					t.Fatal("A completion")
				}
				as, bs := loadHandoffSnapshot(t, ab), loadHandoffSnapshot(t, bb)
				if as.BindingID == bs.BindingID || text(as.Credentials, "token") != "private-context-A" || text(bs.Credentials, "token") != "private-context-B" || len(as.Journal) != 1 || len(bs.Journal) != 1 {
					t.Fatal("owner state isolation")
				}
				t.Log("B3B_STAGE independent B completed while real A guard/callback held; both completed once")
				return
			}
			var effects atomic.Int32
			o, base, backend, ch, auth := b3bFixture(t, "fixture-binding", "fixture-reader", "b3b-private-context", func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
					return AdapterOutput{}, e
				}
				effects.Add(1)
				caller := i.snapshot.Journal[i.journalKey].Caller
				if text(i.Context, "token") != "b3b-private-context" {
					return AdapterOutput{}, errors.New("wrong authority context")
				}
				return AdapterOutput{Result: map[string]any{"value": caller + "-public-result"}}, nil
			})
			before := loadHandoffSnapshot(t, base)
			q := b3bRequest("fixture-binding", "admission-request")
			b3bValid(t, o, q)
			if strings.HasPrefix(name, "B01") {
				auth.deny = "authenticate"
				denied, e := o.AuthenticateChannel(context.Background(), "alice")
				if e == nil || ErrorCode(e) != PermissionDenied || denied != nil || auth.authenticate.Load() != 1 || backend.counts() != [4]int32{} || effects.Load() != 0 || !sameSnapshot(before, loadHandoffSnapshot(t, base)) {
					t.Fatal("Authenticate denial boundary")
				}
				return
			}
			if strings.HasPrefix(name, "B02") {
				empty := NewSnapshot("fixture-binding")
				emptyBase, e := NewMemoryBackend(empty)
				if e != nil {
					t.Fatal(e)
				}
				base = emptyBase
				backend.Backend = emptyBase
				before = loadHandoffSnapshot(t, base)
				out, e := o.Handle(context.Background(), ch, b3bValid(t, o, q))
				b3bFailed(t, out, e, Code("enrollment_required"))
				if backend.loads.Load() < 1 || backend.commits.Load() != 0 || effects.Load() != 0 || !sameSnapshot(before, loadHandoffSnapshot(t, base)) {
					t.Fatal("implicit enrollment/write")
				}
				return
			}
			if strings.HasPrefix(name, "B07") {
				other, otherBase, otherBackend, _, otherAuth := b3bFixture(t, "fixture-binding", "fixture-reader", "other-private-context", func(context.Context, *Invocation) (AdapterOutput, error) {
					t.Error("foreign channel reached adapter")
					return AdapterOutput{}, nil
				})
				otherBefore := loadHandoffSnapshot(t, otherBase)
				out, e := other.Handle(context.Background(), ch, b3bValid(t, other, q))
				if e == nil || ErrorCode(e) != PermissionDenied || len(out) != 0 || otherAuth.allow.Load() != 0 || otherBackend.counts() != [4]int32{} || !sameSnapshot(before, loadHandoffSnapshot(t, base)) || !sameSnapshot(otherBefore, loadHandoffSnapshot(t, otherBase)) {
					t.Fatal("foreign owner channel crossed authority")
				}
				return
			}
			if strings.HasPrefix(name, "B09") {
				q["requestId"] = "same-id"
				raw := b3bValid(t, o, q)
				aRaw, e := o.Handle(context.Background(), ch, raw)
				a := resultMap(t)(aRaw, e)
				bob, e := o.AuthenticateChannel(context.Background(), "bob")
				if e != nil {
					t.Fatal(e)
				}
				bRaw, e := o.Handle(context.Background(), bob, raw)
				b := resultMap(t)(bRaw, e)
				s := loadHandoffSnapshot(t, base)
				aj, bj := s.Journal["alice\x00same-id"], s.Journal["bob\x00same-id"]
				if a["kind"] != "performed" || b["kind"] != "performed" || text(mapValue(a["result"]), "value") != "alice-public-result" || text(mapValue(b["result"]), "value") != "bob-public-result" || effects.Load() != 2 || auth.allow.Load() != 2 || len(s.Journal) != 2 || aj == nil || bj == nil || aj.ReceiptID == bj.ReceiptID || aj.Dispatches != 1 || bj.Dispatches != 1 || text(s.Credentials, "token") != "b3b-private-context" || strings.Contains(string(aRaw)+string(bRaw), "b3b-private-context") {
					t.Fatal("cross-caller cache/journal/context leak")
				}
				t.Log("B3B_STAGE both separately authorized callers executed once, with distinct journals/receipts and their own public result")
				return
			}
			expected := PermissionDenied
			early := false
			switch name {
			case "B03_allow_operation_denied":
				auth.deny = "allow"
			case "B04_allow_target_scope_denied":
				q["target"] = map[string]any{"id": "other-target"}
				b3bValid(t, o, q)
				auth.deny = "target"
			case "B05_confirm_denied":
				q["operationId"] = "fixture.write"
				q["confirmationRef"] = "approval-1"
				b3bValid(t, o, q)
				auth.deny = "confirm"
			case "B06_recover_authorization_denied":
				q = fixtureRequest("recover")
				q["mode"] = "new"
				q["expectedGeneration"] = "epoch-1"
				q["cause"] = "authorized_replacement"
				q["rejectedRequestId"] = nil
				q["recoveryAuthorizationRef"] = "policy-1"
				b3bValid(t, o, q)
				auth.deny = "recovery"
			case "B08_wrong_binding_request":
				q["bindingId"] = "other-binding"
				if e := o.registry.schemas.Validate(schemaPrefix+"request", normalize(q)); e != nil {
					t.Fatal("wrong-binding shape invalid", e)
				}
				early = true
			case "B28_public_cap_override":
				q["maxRpcMs"] = 999999
				expected = ProtocolMismatch
				early = true
			default:
				t.Fatal("unknown admission case")
			}
			raw, e := o.Handle(context.Background(), ch, rawJSON(t, q))
			if early {
				if e == nil || ErrorCode(e) != expected || len(raw) != 0 || auth.allow.Load() != 0 {
					t.Fatal("invalid public selector/binding reached admission")
				}
			} else {
				b3bFailed(t, raw, e, expected)
				if auth.allow.Load() != 1 {
					t.Fatal("Allow not reached once")
				}
				if name == "B05_confirm_denied" && auth.confirm.Load() != 1 {
					t.Fatal("Confirm not reached")
				}
				if name == "B06_recover_authorization_denied" && auth.recovery.Load() != 1 {
					t.Fatal("Recovery authorization not reached")
				}
				if strings.HasPrefix(name, "B03") || strings.HasPrefix(name, "B04") {
					auth.mu.Lock()
					caller, op, target := auth.lastCaller, auth.lastOperation, auth.lastTarget
					auth.mu.Unlock()
					if caller != "alice" || op != "fixture.read" || name == "B04_allow_target_scope_denied" && target != "other-target" || auth.confirm.Load() != 0 || auth.recovery.Load() != 0 {
						t.Fatal("wrong Allow scope/stage")
					}
				}
			}
			if backend.counts() != [4]int32{} || effects.Load() != 0 || !sameSnapshot(before, loadHandoffSnapshot(t, base)) {
				t.Fatal("pre-admission denial wrote/called adapter")
			}
			if early && payloadDigest(o.registry.policy) != payloadDigest(b3bRegistry(t, "fixture-binding", "fixture-reader").policy) {
				t.Fatal("caller changed registered policy/caps")
			}
			t.Logf("B3B_STAGE auth=%d allow=%d confirm=%d recovery=%d backend=%v effects=%d", auth.authenticate.Load(), auth.allow.Load(), auth.confirm.Load(), auth.recovery.Load(), backend.counts(), effects.Load())
		})
	}
}
