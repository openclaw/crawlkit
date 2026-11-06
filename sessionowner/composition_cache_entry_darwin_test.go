//go:build darwin

package sessionowner

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

type cacheBackendObserver struct {
	Backend
	calls int
}

func (b *cacheBackendObserver) Acquire(ctx context.Context) (*Guard, error) {
	b.calls++
	return b.Backend.Acquire(ctx)
}
func (b *cacheBackendObserver) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	b.calls++
	return b.Backend.Load(ctx, g)
}
func (b *cacheBackendObserver) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	b.calls++
	return b.Backend.Commit(ctx, g, e, s, id)
}
func (b *cacheBackendObserver) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	b.calls++
	return b.Backend.Reconcile(ctx, g, id)
}

type cacheAuthorizer struct {
	fixtureAuth
	allowCalls, confirmCalls, recoveryCalls int
	deny                                    bool
}

func (a *cacheAuthorizer) Allow(ctx context.Context, caller string, op Operation, v map[string]any) error {
	a.allowCalls++
	if a.deny {
		return errors.New("fixture-denied")
	}
	return a.fixtureAuth.Allow(ctx, caller, op, v)
}
func (a *cacheAuthorizer) Confirm(ctx context.Context, caller, ref string, op Operation, v map[string]any) error {
	a.confirmCalls++
	return a.fixtureAuth.Confirm(ctx, caller, ref, op, v)
}
func (a *cacheAuthorizer) Recovery(ctx context.Context, caller, ref string, r *Request) error {
	a.recoveryCalls++
	return a.fixtureAuth.Recovery(ctx, caller, ref, r)
}
func callCachedRoute(o *Owner, ordinary bool, ctx context.Context, ch *Channel, raw []byte) ([]byte, error) {
	if ordinary {
		return o.Handle(ctx, ch, raw)
	}
	return o.HandleWithRecovery(ctx, ch, raw)
}
func TestB2B2BPreEntryKnownCache(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		route := "explicit"
		if ordinary {
			route = "ordinary"
		}
		for _, late := range []string{"live", "cancel", "expiry", "caller-expiry", "both"} {
			for _, known := range []string{"completed", "uncertain", "not_dispatched"} {
				t.Run(route+"/"+late+"/"+known, func(t *testing.T) {
					mode := "recover"
					if known == "uncertain" {
						mode = "throttled"
					}
					o, b, ch, clock, counts := compositionFixture(t, mode)
					if known == "not_dispatched" {
						g, _ := b.Acquire(context.Background())
						s, _ := b.Load(context.Background(), g)
						expected := s.Expected()
						s.AuthBlocked = true
						if _, e := b.Commit(context.Background(), g, expected, s, "known-auth-stop"); e != nil {
							t.Fatal(e)
						}
						g.Close()
					}
					q := compositionRequest(o)
					good := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
					if good["certainty"] != known {
						t.Fatal("known fixture certainty", good)
					}
					history := loadHandoffSnapshot(t, b)
					before := *counts
					observer := &cacheBackendObserver{Backend: b}
					o.backend = observer
					auth := &cacheAuthorizer{}
					o.auth = auth
					hits := 0
					o.compositionBoundary = func(point string) {
						if point == "pre_entry_cache" {
							hits++
						}
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if late == "caller-expiry" {
						var stop context.CancelFunc
						ctx, stop = o.contextUntil(ctx, clock.Now().Add(time.Millisecond))
						defer stop()
						clock.Advance(clock.Now().Add(time.Millisecond))
					}
					if late == "cancel" || late == "both" {
						cancel()
					}
					if late == "expiry" || late == "both" {
						deadline, _ := time.Parse("2006-01-02T15:04:05.000Z", text(q, "deadline"))
						clock.Advance(deadline)
					}
					got := resultMap(t)(callCachedRoute(o, ordinary, ctx, ch, rawJSON(t, q)))
					if late == "live" {
						if !equalJSON(good, got) || hits != 0 {
							t.Fatal("positive delivery changed")
						}
					} else if got["kind"] != "failed" || got["certainty"] != known || got["receiptRef"] != good["receiptRef"] || got["requestId"] != good["requestId"] || hits != 1 {
						t.Fatal("late known cache lost original outcome", got, hits)
					}
					if auth.allowCalls != 1 || observer.calls != 0 || *counts != before || !sameSnapshot(history, loadHandoffSnapshot(t, b)) {
						t.Fatal("cache bypassed authorization or did backend/business work")
					}
					t.Logf("route=%s late=%s known=%s hits=%d backend=%d receiptSame=%v", route, late, known, hits, observer.calls, got["receiptRef"] == good["receiptRef"])
				})
			}
		}
	}
}
func TestB2B2BCachePrivacyBeforeRefusal(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		route := "explicit"
		if ordinary {
			route = "ordinary"
		}
		for _, mode := range []string{"deny-allow", "caller", "channel", "binding", "target", "args", "request-id", "invalid", "unenrolled", "confirmation", "recovery"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				o, b, ch, _, counts := compositionFixture(t, "recover")
				q := compositionRequest(o)
				good := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
				if good["kind"] != "performed" {
					t.Fatal("setup")
				}
				history := loadHandoffSnapshot(t, b)
				before := *counts
				bad := cloneMap(q)
				auth := &cacheAuthorizer{}
				o.auth = auth
				switch mode {
				case "deny-allow":
					auth.deny = true
				case "caller":
					ch, _ = o.AuthenticateChannel(context.Background(), "bob")
				case "channel":
					ch, _ = o.AuthenticateChannel(context.Background(), "alice")
				case "binding":
					bad["bindingId"] = "other-binding"
				case "target":
					bad["target"] = map[string]any{"id": "other-target"}
				case "args":
					bad["args"] = map[string]any{"cursor": "other-cursor"}
				case "request-id":
					bad["requestId"] = newID()
				case "invalid":
					bad["unregistered-field"] = true
				case "unenrolled":
					delete(o.readRecovery, operationKey("fixture.read", 1, "perform"))
				case "confirmation":
					bad["operationId"] = "fixture.write"
					bad["confirmationRef"] = "denied-ref"
				case "recovery":
					bad = b2Request("new", "")
					bad["requestId"] = q["requestId"]
					bad["recoveryAuthorizationRef"] = "denied-ref"
				}
				observer := &cacheBackendObserver{Backend: b}
				o.backend = observer
				hits := 0
				o.compositionBoundary = func(point string) {
					if point == "pre_entry_cache" {
						hits++
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				raw, e := callCachedRoute(o, ordinary, ctx, ch, rawJSON(t, bad))
				if e == nil {
					got := resultMap(t)(raw, nil)
					if got["kind"] != "failed" || got["receiptRef"] == good["receiptRef"] {
						t.Fatal("foreign/mismatched cache reflected", mode)
					}
				}
				if bytes.Contains(raw, []byte(text(good, "receiptRef"))) || hits != 0 || observer.calls != 0 || *counts != before || !sameSnapshot(history, loadHandoffSnapshot(t, b)) {
					t.Fatal("privacy gate bypass", mode)
				}
				if ordinary && mode == "confirmation" && auth.confirmCalls != 1 {
					t.Fatal("confirmation bypassed")
				}
				if ordinary && mode == "recovery" && auth.recoveryCalls != 1 {
					t.Fatal("recovery authorization bypassed")
				}
				if mode == "deny-allow" && auth.allowCalls != 1 {
					t.Fatal("Allow bypassed")
				}
			})
		}
	}
}
func TestB2B2BPendingCacheNotPromoted(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		name := "explicit"
		if ordinary {
			name = "ordinary"
		}
		t.Run(name, func(t *testing.T) {
			o, b, ch, _, counts := compositionFixture(t, "recover")
			fault := &composedOutcomeFault{Backend: b, target: "final", stored: false}
			o.backend = fault
			q := compositionRequest(o)
			first := resultMap(t)(o.HandleWithRecovery(context.Background(), ch, rawJSON(t, q)))
			if !fault.hit || first["kind"] != "failed" {
				t.Fatal("pending setup")
			}
			history := loadHandoffSnapshot(t, b)
			before := *counts
			observer := &cacheBackendObserver{Backend: fault}
			o.backend = observer
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			got := resultMap(t)(callCachedRoute(o, ordinary, ctx, ch, rawJSON(t, q)))
			if got["kind"] != "failed" || got["certainty"] == "completed" || observer.calls != 0 || *counts != before || !sameSnapshot(history, loadHandoffSnapshot(t, b)) {
				t.Fatal("pending cache promoted or mutated")
			}
		})
	}
}
