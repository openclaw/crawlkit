package sessionowner

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The follower attempts the same MemoryBackend gate while the first invocation
// owns it. A failed nonblocking acquisition is observed before allowing release;
// the subsequent wait uses the original queue context, not a second lock.
type b2c1QueuedBackend struct {
	*MemoryBackend
	acquires atomic.Int32
	blocked  chan bool
}

func (b *b2c1QueuedBackend) Acquire(ctx context.Context) (*Guard, error) {
	if b.acquires.Add(1) != 2 {
		return b.MemoryBackend.Acquire(ctx)
	}
	select {
	case <-b.gate:
		b.blocked <- false
		return NewGuard(b.MemoryBackend, func() error { b.gate <- struct{}{}; return nil }), nil
	default:
		b.blocked <- true
	}
	select {
	case <-ctx.Done():
		return nil, fail(OwnerBusy, "lock")
	case <-b.gate:
		return NewGuard(b.MemoryBackend, func() error { b.gate <- struct{}{}; return nil }), nil
	}
}

func TestB2C1RotationCursor(t *testing.T) {
	for _, name := range []string{"actually_queued_rotation", "journal_revision_delta", "stale_credential_base", "current_credential_base", "cursor_utf8_exact", "cursor_utf8_over"} {
		t.Run(name, func(t *testing.T) {
			if name == "actually_queued_rotation" {
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				var calls atomic.Int32
				var firstRevision, followerRevision, followerCredential uint64
				var followerToken string
				o, b, ch := fixtureStateOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
					n := calls.Add(1)
					if n == 1 {
						if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
							return AdapterOutput{}, e
						}
						firstRevision = i.snapshot.StateRevision
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return AdapterOutput{}, ctx.Err()
						}
						return AdapterOutput{Result: map[string]any{"value": "public-first"}, CredentialDelta: map[string]any{"token": "queued-rotated-private"}}, nil
					}
					followerRevision, followerCredential, followerToken = i.snapshot.StateRevision, i.snapshot.CredentialRevision, text(i.Context, "token")
					if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
						return AdapterOutput{}, e
					}
					return AdapterOutput{Result: map[string]any{"value": "public-following"}}, nil
				})
				queued := &b2c1QueuedBackend{MemoryBackend: b, blocked: make(chan bool, 1)}
				o.backend = queued
				q1, q2 := fixtureRequest("perform"), fixtureRequest("perform")
				q1["requestId"], q2["requestId"] = "queued-first", "queued-follower"
				type response struct {
					raw []byte
					err error
				}
				done := make(chan response, 2)
				run := func(raw []byte) { out, e := o.Handle(context.Background(), ch, raw); done <- response{out, e} }
				go run(rawJSON(t, q1))
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("first owner did not acquire")
				}
				go run(rawJSON(t, q2))
				select {
				case blocked := <-queued.blocked:
					if !blocked || calls.Load() != 1 {
						t.Fatal("follower was not actually queued behind held guard")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("follower did not attempt acquisition")
				}
				unblock()
				for n := 0; n < 2; n++ {
					select {
					case result := <-done:
						got := resultMap(t)(result.raw, result.err)
						if got["kind"] != "performed" || got["finalGeneration"] != "epoch-1" {
							t.Fatal("queued operation failed or changed epoch", got["kind"])
						}
					case <-time.After(5 * time.Second):
						t.Fatal("queued requests failed to settle")
					}
				}
				if calls.Load() != 2 || followerToken != "queued-rotated-private" || followerCredential != 1 || followerRevision <= firstRevision {
					t.Fatal("queued follower did not snapshot latest credentials")
				}
				g, e := b.Acquire(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				s, e := b.Load(context.Background(), g)
				g.Close()
				if e != nil {
					t.Fatal(e)
				}
				if s.Generation != "epoch-1" || s.CredentialRevision != 1 || len(s.Journal) != 2 {
					t.Fatal("rotation/history changed")
				}
				for _, j := range s.Journal {
					if j.Dispatches != 1 || j.UnresolvedIntentID != "" || j.Certainty != "completed" {
						t.Fatal("queued history/permit changed")
					}
				}
				return
			}
			if strings.HasPrefix(name, "cursor_") {
				reached := 0
				o, _, ch := fixtureStateOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
					reached++
					limit := i.Operation().WireCaps.MaxCursorBytes
					cursor := strings.Repeat("é", limit/2) + strings.Repeat("x", limit%2)
					if name == "cursor_utf8_over" {
						cursor += "x"
					}
					before, err := i.snapshot.Clone()
					if err != nil {
						t.Fatal(err)
					}
					err = i.AcceptCursor(cursor)
					if name == "cursor_utf8_over" {
						if err == nil || ErrorCode(err) != PayloadTooLarge {
							t.Fatal("byte-over cursor admitted")
						}
					} else if err != nil {
						t.Fatal("exact multibyte cursor refused", err)
					}
					if len(cursor) != limit+map[bool]int{true: 1, false: 0}[name == "cursor_utf8_over"] || !sameSnapshot(before, i.snapshot) || i.dispatched() {
						t.Fatal("cursor mutated state or fixture not exact")
					}
					if err != nil {
						return AdapterOutput{}, err
					}
					if e := i.BeforeEffect(ctx, "EXECUTING"); e != nil {
						return AdapterOutput{}, e
					}
					return AdapterOutput{Result: map[string]any{"value": "public"}}, nil
				})
				got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, fixtureRequest("perform"))))
				want := "performed"
				if name == "cursor_utf8_over" {
					want = "failed"
				}
				if reached != 1 || got["kind"] != want {
					t.Fatal("cursor stage not reached or wrong outcome")
				}
				return
			}
			var backend *b2c1ObservedBackend
			reached, deltaCalls := 0, 0
			o, b, ch := fixtureStateOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				reached++
				session, e := NewAdapterSession(i, "b2c1-delta-parent")
				if e != nil {
					t.Fatal(e)
				}
				defer session.cancelParent()
				if _, e = session.InvokeFrame(); e != nil {
					t.Fatal(e)
				}
				baseState := text(session.lease, "baseStateRevision")
				baseCredential := text(session.lease, "baseCredentialRevision")
				if ack, _, e := session.Accept(ctx, rawJSON(t, b2c1Checkpoint(t, session))); e != nil || len(ack) == 0 {
					t.Fatal("delta fixture checkpoint missing", e)
				}
				if name == "journal_revision_delta" {
					checkpoint := b2c1Checkpoint(t, session)
					checkpoint["sequence"] = "1"
					checkpoint["event"] = "after_effect"
					checkpoint["phase"] = "EXECUTE_RESULT"
					checkpoint["effectCertainty"] = "provider_effect_known"
					if ack, _, e := session.Accept(ctx, rawJSON(t, checkpoint)); e != nil || len(ack) == 0 {
						t.Fatal("journal-only checkpoint failed", e)
					}
				}
				if baseState == strconv.FormatUint(i.snapshot.StateRevision, 10) || baseCredential != strconv.FormatUint(i.snapshot.CredentialRevision, 10) {
					t.Fatal("state-only revision fixture not reached")
				}
				if name == "stale_credential_base" {
					if e := i.owner.applyDelta(ctx, i, map[string]any{"token": "newer-owner-private"}); e != nil {
						t.Fatal("owner rotation not reached", e)
					}
					if baseCredential == strconv.FormatUint(i.snapshot.CredentialRevision, 10) {
						t.Fatal("credential base did not become stale")
					}
				}
				before, e := i.snapshot.Clone()
				if e != nil {
					t.Fatal(e)
				}
				calls := backend.calls()
				ack, out, e := session.Accept(ctx, rawJSON(t, b2c1PrivateResult(session, map[string]any{"token": "private-result-rotation"})))
				if name == "stale_credential_base" {
					if e == nil {
						t.Error("stale credential base admitted")
						if out == nil {
							t.Fatal("missing unexpected admitted output")
						}
						return *out, nil
					}
					b2c1ErrorDetail(t, e, "credential_base")
					if ack != nil || out != nil || calls != backend.calls() || !sameSnapshot(before, i.snapshot) {
						t.Fatal("stale delta mutated/admitted")
					}
					return AdapterOutput{}, e
				}
				if e != nil || ack != nil || out == nil || !sameSnapshot(before, i.snapshot) || calls != backend.calls() {
					t.Fatal("valid unchanged-credential result refused or committed prematurely", e)
				}
				return *out, nil
			})
			backend = &b2c1ObservedBackend{Backend: b}
			o.backend = backend
			o.delta = func(ctx context.Context, s *Snapshot, d map[string]any) (map[string]any, error) {
				deltaCalls++
				return d, nil
			}
			got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, fixtureRequest("perform"))))
			g, e := b.Acquire(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			s, e := b.Load(context.Background(), g)
			g.Close()
			if e != nil {
				t.Fatal(e)
			}
			wantKind, wantToken := "performed", "private-result-rotation"
			if name == "stale_credential_base" {
				wantKind, wantToken = "failed", "newer-owner-private"
			}
			if reached != 1 || got["kind"] != wantKind || s.Generation != "epoch-1" || s.CredentialRevision != 1 || deltaCalls != 1 || text(s.Credentials, "token") != wantToken {
				t.Fatal("credential base/history outcome", name, got["kind"], s.CredentialRevision, deltaCalls)
			}
			if len(s.Journal) != 1 {
				t.Fatal("credential result replaced operation history")
			}
			for _, j := range s.Journal {
				if j.Dispatches != 1 || j.UnresolvedIntentID != "" {
					t.Fatal("credential refusal refunded or replaced the original permit")
				}
				if name == "stale_credential_base" && j.Certainty != "uncertain" {
					t.Fatal("stale delta refusal lost the known issued-effect uncertainty")
				}
			}
		})
	}
}
