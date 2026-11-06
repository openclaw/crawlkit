//go:build darwin

package sessionowner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const c2PrivateMarker = "C2_PRIVATE_BACKEND_MARKER"

type c2FallbackBackend struct {
	Backend
	t                                                                                     *testing.T
	now                                                                                   func() time.Time
	start                                                                                 time.Time
	inner, outer                                                                          *Guard
	control, root                                                                         context.Context
	prep, resolution, row, selectedID                                                     string
	releaseErr, firstLoadErr                                                              error
	onRelease                                                                             func()
	trigger                                                                               func(context.Context)
	selected, releaseCalls, released, loads, commits, lateCommits, reconciles, priorReads int
	fault, fallback, refused                                                              bool
	reads                                                                                 map[string]int
	events                                                                                []string
}

func (b *c2FallbackBackend) bound(ctx context.Context, queue bool) {
	b.t.Helper()
	d, ok := ctx.Deadline()
	want := b.start.Add(readSettlementBudget)
	if queue {
		want = b.start.Add(readSettlementQueueBudget)
	}
	if !ok || !d.Equal(want) {
		b.t.Fatalf("wrong current control/queue bound: %v want %v", d, want)
	}
	if !queue {
		b.control = ctx
		if b.root == nil {
			b.root = ctx
		}
	}
}
func (b *c2FallbackBackend) Acquire(ctx context.Context) (*Guard, error) {
	b.bound(ctx, true)
	g, e := b.Backend.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	b.inner = g
	b.outer = NewGuard(b, func() error {
		b.releaseCalls++
		e := g.Close()
		if e != nil {
			return e
		}
		if g.Check(b.Backend) == nil {
			b.t.Fatal("actual inner guard still open")
		}
		b.released++
		b.events = append(b.events, "inner_released")
		if b.onRelease != nil {
			b.onRelease()
		}
		if b.releaseErr != nil {
			b.refused = true
			return b.releaseErr
		}
		return nil
	})
	return b.outer, nil
}
func (b *c2FallbackBackend) guard(g *Guard) {
	if g != b.outer || g.Check(b) != nil || b.inner == nil {
		b.t.Fatal("wrong test guard translation")
	}
}
func (b *c2FallbackBackend) hit(ctx context.Context, stage string) {
	b.selected++
	if b.selected != 1 {
		b.t.Fatal("selected callback repeated")
	}
	b.priorReads = b.reconciles
	b.events = append(b.events, stage)
	b.refused = true
	if b.trigger != nil {
		b.trigger(ctx)
	}
}
func (b *c2FallbackBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	b.guard(g)
	b.bound(ctx, false)
	b.loads++
	if b.loads == 1 && b.firstLoadErr != nil {
		b.hit(ctx, "initial_load_error")
		return nil, b.firstLoadErr
	}
	s, e := b.Backend.Load(ctx, b.inner)
	if e != nil {
		return s, e
	}
	if b.fault && b.fallback && (b.row == "notcommitted_fallback_load_late" || b.row == "positive_fallback_receipt_finalLoaderror") {
		b.fallback = false
		b.hit(ctx, "fallback_load")
		if b.row == "positive_fallback_receipt_finalLoaderror" {
			return nil, errors.New(c2PrivateMarker + "/load")
		}
	}
	return s, nil
}
func (b *c2FallbackBackend) Commit(ctx context.Context, g *Guard, x Expected, s *Snapshot, id string) (CommitReceipt, error) {
	b.guard(g)
	b.bound(ctx, false)
	if b.refused {
		b.lateCommits++
	}
	b.commits++
	if id == b.prep && !b.fault && b.row != "" {
		switch b.row {
		case "committed_lostACK_reconcile_late", "notcommitted_fallback_load_late", "fallback_reconcile_IOerror_after_cancel", "positive_fallback_receipt_finalLoaderror":
			b.fault = true
			b.events = append(b.events, "original_preparation_attempt")
			if b.row != "notcommitted_fallback_load_late" {
				r, e := b.Backend.Commit(ctx, b.inner, x, s, id)
				if e != nil || r.Certainty != "committed" {
					b.t.Fatal("preparation did not really commit", e)
				}
			}
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, nil
		}
	}
	return b.Backend.Commit(ctx, b.inner, x, s, id)
}
func (b *c2FallbackBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	b.guard(g)
	b.bound(ctx, false)
	b.reconciles++
	b.reads[id]++
	r, e := b.Backend.Reconcile(ctx, b.inner, id)
	if e != nil {
		return r, e
	}
	if b.fault && id == b.prep && !b.fallback {
		b.fallback = true
		b.events = append(b.events, "native_fallback_"+r.Certainty)
		switch b.row {
		case "committed_lostACK_reconcile_late":
			if r.Certainty != "committed" {
				b.t.Fatal("positive fallback missing")
			}
			b.hit(ctx, "fallback_reconcile_late")
		case "fallback_reconcile_IOerror_after_cancel":
			b.hit(ctx, "fallback_reconcile_error")
			return CommitReceipt{}, errors.New(c2PrivateMarker + "/reconcile")
		}
	}
	if b.selectedID != "" && id == b.selectedID {
		b.hit(ctx, "later_original_dependency")
	}
	return r, nil
}

func TestB2C2SettlementFinalization(t *testing.T) {
	rows := []struct {
		name  string
		modes []string
	}{
		{"committed_lostACK_reconcile_late", []string{"caller", "expiry"}},
		{"notcommitted_fallback_load_late", []string{"channel", "expiry"}},
		{"fallback_reconcile_IOerror_after_cancel", []string{"caller", "channel"}},
		{"positive_fallback_receipt_finalLoaderror", []string{"live", "caller_canceled"}},
		{"release_error_after_positive", []string{"settled", "historical"}},
		{"release_error_and_prior_body_error", []string{"PermissionDenied", "rawStorageFailure", "unknown_typedCode_privacy"}},
		{"release_error_and_lifetime", []string{"settled_expiry", "historical_channel"}},
		{"blocked_or_triple_error_precedence", []string{"liveblocked_release", "permission_cancel_release"}},
		{"nonemptyClaims_Plan_mutation_allow", []string{"allow"}},
		{"nonemptyClaims_Plan_mutation_deny", []string{"deny"}},
		{"blocked_late_guard_release", []string{"caller", "channel", "expiry"}},
		{"single_staged_candidate_or_prior_laterdependency_sentinel", []string{"channel", "expiry"}},
	}
	for _, row := range rows {
		for _, mode := range row.modes {
			t.Run(row.name+"/"+mode, func(t *testing.T) {
				target, stored := "initial-intent", false
				if strings.Contains(mode, "historical") {
					target, stored = "final", true
				}
				if row.name == "blocked_late_guard_release" || mode == "liveblocked_release" {
					stored = true
				}
				if strings.HasPrefix(row.name, "nonemptyClaims") {
					target = "retry-intent"
				}
				var o *Owner
				var base Backend
				var ch *Channel
				var q map[string]any
				var counts *compositionCounts
				var key string
				if strings.HasPrefix(row.name, "single_staged") {
					o, base, ch, q, counts, key = stagedSettlementGraph(t)
				} else {
					o, base, ch, q, counts = settlementFixture(t, target, stored)
					key = "alice\x00" + text(q, "requestId")
				}
				before := loadHandoffSnapshot(t, base)
				prior := *counts
				prep, resolution := settlementIDs(before, before.Journal[key])
				clock := &a2Clock{now: o.now()}
				o.now, o.deadlineHook = clock.Now, clock.Until
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				wrapper := &c2FallbackBackend{Backend: base, t: t, now: o.now, start: o.now(), prep: prep, resolution: resolution, row: row.name, reads: map[string]int{}}
				lifetimeMode := mode
				if strings.Contains(mode, "expiry") {
					lifetimeMode = "expiry"
				}
				if strings.Contains(mode, "channel") {
					lifetimeMode = "channel"
				}
				if strings.Contains(mode, "caller") || mode == "permission_cancel_release" {
					lifetimeMode = "caller"
				}
				ended := false
				end := func(control context.Context) {
					if ended {
						t.Fatal("lifetime trigger repeated")
					}
					ended = true
					switch lifetimeMode {
					case "caller":
						cancel()
					case "channel":
						ch.Close()
					case "expiry":
						d, ok := control.Deadline()
						if !ok {
							t.Fatal("no current deadline")
						}
						clock.Advance(d)
					default:
						t.Fatal("invalid lifetime trigger", lifetimeMode)
					}
				}
				capabilities := 0
				descriptorHit := false
				o.settlementAuthorization = func(control context.Context, d ReadSettlementDescriptor) error {
					capabilities++
					wrapper.control = control
					wrapper.bound(control, false)
					if strings.HasPrefix(row.name, "nonemptyClaims") {
						if len(d.Claims) == 0 || d.Plan.PhaseMaxMs == nil {
							t.Fatal("nonempty descriptor fixture missing")
						}
						original := d.Claims[0]
						if before.Claims[original] == nil {
							t.Fatal("descriptor claim not original")
						}
						d.Claims[0] = c2PrivateMarker
						d.Plan.PhaseMaxMs["action"] = 999999
						descriptorHit = true
						if mode == "deny" {
							return errors.New(c2PrivateMarker + "/deny")
						}
						return nil
					}
					if mode == "PermissionDenied" || mode == "permission_cancel_release" {
						wrapper.hit(control, "capability_denial")
						if mode == "permission_cancel_release" {
							end(control)
						}
						return errors.New(c2PrivateMarker + "/permission")
					}
					return nil
				}
				expectedCode := Code("")
				expectedDetail := ""
				expectedState := ""
				freshPositive := false
				switch row.name {
				case "committed_lostACK_reconcile_late", "notcommitted_fallback_load_late", "fallback_reconcile_IOerror_after_cancel":
					wrapper.trigger = end
					expectedCode = Canceled
					if lifetimeMode == "expiry" {
						expectedCode = "deadline_exceeded"
					}
				case "positive_fallback_receipt_finalLoaderror":
					if mode == "caller_canceled" {
						wrapper.trigger = end
						expectedCode = Canceled
					} else {
						expectedState = "blocked"
					}
				case "release_error_after_positive":
					wrapper.releaseErr = errors.New(c2PrivateMarker + "/release")
					expectedCode = StorageFailure
					expectedDetail = "settlement_guard_release"
					freshPositive = true
				case "release_error_and_prior_body_error":
					wrapper.releaseErr = errors.New(c2PrivateMarker + "/release")
					switch mode {
					case "PermissionDenied":
						expectedCode = PermissionDenied
						expectedDetail = "settlement_authorization_guard_release"
					case "rawStorageFailure":
						wrapper.firstLoadErr = errors.New(c2PrivateMarker + "/raw")
						expectedCode = StorageFailure
						expectedDetail = "settlement_storage_guard_release"
					case "unknown_typedCode_privacy":
						wrapper.firstLoadErr = &Error{Code: Code(c2PrivateMarker + "_CODE"), Detail: c2PrivateMarker + "/detail"}
						expectedCode = StorageFailure
						expectedDetail = "settlement_storage_guard_release"
					}
				case "release_error_and_lifetime":
					wrapper.releaseErr = errors.New(c2PrivateMarker + "/release")
					wrapper.onRelease = func() {
						if wrapper.released != 1 {
							t.Fatal("lifetime before actual release")
						}
						wrapper.hit(wrapper.control, "release_then_lifetime")
						end(wrapper.control)
					}
					expectedCode = Canceled
					if lifetimeMode == "expiry" {
						expectedCode = "deadline_exceeded"
					}
					freshPositive = true
				case "blocked_or_triple_error_precedence":
					wrapper.releaseErr = errors.New(c2PrivateMarker + "/release")
					if mode == "permission_cancel_release" {
						expectedCode = PermissionDenied
						expectedDetail = "settlement_authorization_guard_release"
					} else {
						expectedCode = StorageFailure
						expectedDetail = "settlement_guard_release"
					}
				case "nonemptyClaims_Plan_mutation_allow":
					expectedState = "settled"
					freshPositive = true
				case "nonemptyClaims_Plan_mutation_deny":
					expectedCode = PermissionDenied
					expectedDetail = "settlement_authorization"
				case "blocked_late_guard_release":
					wrapper.onRelease = func() {
						if wrapper.released != 1 || checkCandidateStorageBound(wrapper.control) != nil {
							t.Fatal("blocked lifetime fixture not live after release")
						}
						wrapper.hit(wrapper.control, "blocked_release_lifetime")
						end(wrapper.control)
					}
					expectedCode = Canceled
					if lifetimeMode == "expiry" {
						expectedCode = "deadline_exceeded"
					}
				case "single_staged_candidate_or_prior_laterdependency_sentinel":
					for _, dep := range before.Journal[key].Composition.Settlement.Dependencies {
						if dep.Role == "claim_prior" {
							wrapper.selectedID = dep.ID
							break
						}
					}
					if wrapper.selectedID == "" {
						t.Fatal("prior dependency absent")
					}
					wrapper.trigger = end
					expectedCode = Canceled
					if mode == "expiry" {
						expectedCode = "deadline_exceeded"
					}
				}
				o.backend = wrapper
				got, err := o.SettleReadComposition(ctx, ch, rawJSON(t, q))
				after := loadHandoffSnapshot(t, base)
				// First check proof of reaching the intended edge and preserving all original
				// business facts, even when the pre-correction return contract is wrong.
				if wrapper.releaseCalls != 1 || wrapper.released != 1 || wrapper.inner.Check(base) == nil || wrapper.lateCommits != 0 || *counts != prior {
					t.Fatal("guard/business/latecommit violation", wrapper.releaseCalls, wrapper.released, wrapper.lateCommits)
				}
				if wrapper.root == nil || wrapper.root.Err() == nil {
					t.Fatal("control context not stopped after release/finalization")
				}
				if strings.HasPrefix(row.name, "nonemptyClaims") && !descriptorHit {
					t.Fatal("descriptor mutation not reached")
				}
				if wrapper.firstLoadErr == nil && capabilities != 1 {
					t.Fatal("capability not reached once", capabilities)
				}
				if wrapper.firstLoadErr != nil && capabilities != 0 {
					t.Fatal("bad initial load reached capability")
				}
				if wrapper.trigger != nil || wrapper.onRelease != nil || wrapper.firstLoadErr != nil || mode == "PermissionDenied" || mode == "permission_cancel_release" {
					if wrapper.selected != 1 {
						t.Fatal("selected edge not reached exactly once", wrapper.selected)
					}
				}
				if row.name == "positive_fallback_receipt_finalLoaderror" && wrapper.selected != 1 {
					t.Fatal("post-positive load error not reached")
				}
				if strings.Contains(row.name, "fallback") || strings.HasPrefix(row.name, "committed_lostACK") {
					if !wrapper.fault || wrapper.reads[prep] < 2 || wrapper.commits != 1 {
						t.Fatal("original fallback transaction not reached", wrapper.fault, wrapper.reads[prep], wrapper.commits)
					}
				}
				if wrapper.selectedID != "" && (wrapper.priorReads < 2 || wrapper.reads[wrapper.selectedID] != 1) {
					t.Fatal("sentinel was not a later exact dependency")
				}
				if after.Journal[key].Composition.Settlement != nil {
					assertSettlementPreserved(t, before, after, key)
				} else if !sameSnapshot(before, after) {
					t.Fatal("refusal changed original state")
				}
				for id, receipt := range before.Commits {
					if !receiptMatches(after.Commits[id], receipt) {
						t.Fatal("original receipt changed")
					}
				}
				if r := after.Journal[key].Composition.Settlement; r != nil {
					if r.PreparationID != prep || r.ResolutionID != resolution {
						t.Fatal("new competing terminal IDs")
					}
				}
				if row.name == "notcommitted_fallback_load_late" {
					if _, ok := after.Commits[prep]; ok {
						t.Fatal("noncommit became committed")
					}
				}
				if strings.HasPrefix(row.name, "committed_lostACK") || row.name == "fallback_reconcile_IOerror_after_cancel" || row.name == "positive_fallback_receipt_finalLoaderror" {
					if r, ok := after.Commits[prep]; !ok || r.Certainty != "committed" {
						t.Fatal("known committed preparation lost")
					}
					if _, ok := after.Commits[resolution]; ok {
						t.Fatal("late resolution occurred")
					}
				}
				if expectedCode != "" {
					if err == nil || !reflect.DeepEqual(got, ReadSettlementStatus{}) {
						t.Fatalf("error must clear every status: row=%s mode=%s status=%+v error=%v", row.name, mode, got, err)
					}
					safe, ok := err.(*Error)
					if !ok || safe.Code != expectedCode {
						t.Fatalf("wrong safe primary error: got=%#v want=%s", err, expectedCode)
					}
					if expectedDetail != "" && safe.Detail != expectedDetail {
						t.Fatalf("wrong fixed diagnostic: got=%s want=%s", safe.Detail, expectedDetail)
					}
					if wrapper.releaseErr != nil && expectedDetail == "" && !strings.HasSuffix(safe.Detail, "_guard_release") {
						t.Fatal("combined lifetime/release diagnostic missing")
					}
				} else if err != nil || got.State != expectedState {
					t.Fatal("live status contract changed", got, err)
				}
				material, _ := json.Marshal(struct {
					Status ReadSettlementStatus
					Error  error
				}{got, err})
				if strings.Contains(string(material)+fmt.Sprintf("%+v %#v", err, err), c2PrivateMarker) {
					t.Fatal("raw/typed backend marker reflected")
				}
				if freshPositive {
					// This is a new explicitly authorized bounded control on a new channel,
					// after the original call has released its guard and delivered its result.
					fresh, newChannel := freshSettlement(t, o, base)
					fresh.candidateBindings = o.candidateBindings
					freshCalls := 0
					fresh.settlementAuthorization = func(control context.Context, d ReadSettlementDescriptor) error {
						freshCalls++
						deadline, ok := control.Deadline()
						if !ok || !deadline.Equal(fresh.now().Add(readSettlementBudget)) {
							t.Fatal("fresh control deadline")
						}
						return nil
					}
					observed, e := fresh.SettleReadComposition(context.Background(), newChannel, rawJSON(t, q))
					want := "settled"
					if strings.Contains(mode, "historical") {
						want = "historical"
					}
					if e != nil || observed.State != want || freshCalls != 1 || *counts != prior || !sameSnapshot(after, loadHandoffSnapshot(t, base)) {
						t.Fatal("separate authorized readback changed history", observed, e)
					}
					if want == "settled" && (observed.PreparationID != prep || observed.ResolutionID != resolution) {
						t.Fatal("fresh readback used new terminal identity")
					}
				}
				t.Logf("selected=%d originalReads=%d commits=%d lateCommits=%d actualReleases=%d events=%v", wrapper.selected, wrapper.reconciles, wrapper.commits, wrapper.lateCommits, wrapper.released, wrapper.events)
			})
		}
	}
}
