package sessionowner

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type b2c1ObservedBackend struct {
	Backend
	loads, commits, reconciles atomic.Int64
}

func (b *b2c1ObservedBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	b.loads.Add(1)
	return b.Backend.Load(ctx, g)
}
func (b *b2c1ObservedBackend) Commit(ctx context.Context, g *Guard, expected Expected, s *Snapshot, id string) (CommitReceipt, error) {
	b.commits.Add(1)
	return b.Backend.Commit(ctx, g, expected, s, id)
}
func (b *b2c1ObservedBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	b.reconciles.Add(1)
	return b.Backend.Reconcile(ctx, g, id)
}
func (b *b2c1ObservedBackend) calls() [3]int64 {
	return [3]int64{b.loads.Load(), b.commits.Load(), b.reconciles.Load()}
}
func b2c1Checkpoint(t *testing.T, session *AdapterSession) map[string]any {
	t.Helper()
	return map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": cloneMap(session.lease), "sequence": "0", "phase": "EXECUTING", "event": "before_effect", "effectCertainty": "not_dispatched", "evidenceId": "b2c1-evidence", "evidence": map[string]any{"stage": "synthetic-before-effect"}}
}
func b2c1PrivateResult(session *AdapterSession, delta map[string]any) map[string]any {
	var value any
	if delta != nil {
		value = delta
	}
	return map[string]any{"protocol": AdapterProtocol, "kind": "adapter_result", "direction": "adapter_to_authority", "lease": cloneMap(session.lease), "sequence": strconv.FormatUint(session.inSeq, 10), "status": "ok", "certainty": "completed", "result": map[string]any{"value": "ordinary-public-output"}, "failure": nil, "credentialDelta": value, "candidate": nil, "proof": nil}
}
func b2c1ErrorDetail(t *testing.T, err error, detail string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Detail != detail {
		t.Fatalf("did not reach %s: %v", detail, err)
	}
}

func TestB2C1PrivateEnvelope(t *testing.T) {
	cases := []string{"valid_checkpoint", "artifact", "nonce", "target_digest", "epoch", "deadline", "sequence_gap", "sequence_replay", "direction", "public_to_private", "private_to_public", "invoke_target", "expired_owner_deadline"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			clock := &a2Clock{now: time.Now().UTC().Truncate(time.Millisecond)}
			var backend *b2c1ObservedBackend
			reached, effects := 0, 0
			o, b, ch := fixtureOwner(t, func(ctx context.Context, i *Invocation) (AdapterOutput, error) {
				reached++
				session, err := NewAdapterSession(i, "b2c1-parent")
				if err != nil {
					t.Fatal(err)
				}
				defer session.cancelParent()
				if name == "invoke_target" {
					before, e := i.snapshot.Clone()
					if e != nil {
						t.Fatal(e)
					}
					calls := backend.calls()
					i.Target = map[string]any{"id": "other-target"}
					frame, e := session.InvokeFrame()
					if e == nil || len(frame) != 0 {
						t.Fatal("target mutation invoked")
					}
					b2c1ErrorDetail(t, e, "invocation_snapshot")
					if calls != backend.calls() || !sameSnapshot(before, i.snapshot) || i.dispatched() {
						t.Fatal("target refusal touched authority")
					}
					return AdapterOutput{}, e
				}
				invoke, err := session.InvokeFrame()
				if err != nil {
					t.Fatal(err)
				}
				checkpoint := b2c1Checkpoint(t, session)
				if e := i.owner.registry.schemas.Validate(schemaPrefix+"adapter", normalize(checkpoint)); e != nil {
					t.Fatal("invalid baseline checkpoint", e)
				}
				if name == "sequence_replay" || name == "valid_checkpoint" {
					ack, out, e := session.Accept(ctx, rawJSON(t, checkpoint))
					if e != nil || len(ack) == 0 || out != nil || !session.permitted.Load() || !i.dispatched() {
						t.Fatal("positive checkpoint failed", e)
					}
					av, e := DecodeJSON(ack, HardFrameLimit)
					if e != nil {
						t.Fatal(e)
					}
					am, _ := object(av)
					if am["authorizedToProceed"] != true || text(am, "checkpointSequence") != "0" || text(am, "durableStateRevision") != strconv.FormatUint(i.snapshot.StateRevision, 10) {
						t.Fatal("ACK not bound to durable checkpoint")
					}
					if name == "valid_checkpoint" {
						effects++
						_, output, e := session.Accept(ctx, rawJSON(t, b2c1PrivateResult(session, nil)))
						if e != nil || output == nil {
							t.Fatal("positive private result failed", e)
						}
						return *output, nil
					}
				}
				before, err := i.snapshot.Clone()
				if err != nil {
					t.Fatal(err)
				}
				calls, sequence, dispatch := backend.calls(), session.inSeq, i.snapshot.Journal[i.journalKey].Dispatches
				lease := mapValue(checkpoint["lease"])
				checkpoint["lease"] = lease
				detail := "lease_sequence"
				privateValid := true
				switch name {
				case "artifact":
					lease["adapterArtifactSha256"] = Hash([]byte("other-artifact"))
				case "nonce":
					lease["leaseId"] = "other-nonce"
				case "target_digest":
					request := cloneMap(i.request.Value)
					request["target"] = map[string]any{"id": "other-target"}
					parsed, e := i.owner.registry.ParseRequest(rawJSON(t, request))
					if e != nil {
						t.Fatal(e)
					}
					if parsed.Digest == i.request.Digest {
						t.Fatal("target did not alter digest")
					}
					lease["operationDigest"] = parsed.Digest
				case "epoch":
					lease["authorityGeneration"] = "other-epoch"
				case "deadline":
					deadline, e := time.Parse("2006-01-02T15:04:05.000Z", text(lease, "deadline"))
					if e != nil {
						t.Fatal(e)
					}
					lease["deadline"] = timestamp(deadline.Add(time.Second))
				case "sequence_gap":
					checkpoint["sequence"] = "1"
				case "sequence_replay":
				case "direction":
					v, e := DecodeJSON(invoke, HardFrameLimit)
					if e != nil {
						t.Fatal(e)
					}
					checkpoint, _ = object(v)
				case "public_to_private":
					checkpoint = fixtureRequest("inspect")
					if _, e := i.owner.registry.ParseRequest(rawJSON(t, checkpoint)); e != nil {
						t.Fatal("not valid public input", e)
					}
					privateValid, detail = false, ""
				case "private_to_public":
					if _, e := i.owner.registry.ParseRequest(rawJSON(t, checkpoint)); e == nil {
						t.Fatal("private frame admitted publicly")
					}
					// The real public entrypoint must reject at parsing, before
					// attempting storage or recursively waiting for this guard.
					if raw, e := i.owner.Handle(ctx, i.active.channelRef, rawJSON(t, checkpoint)); e == nil || len(raw) != 0 {
						t.Fatal("public owner admitted a private envelope")
					}
					if calls != backend.calls() || !sameSnapshot(before, i.snapshot) || i.dispatched() {
						t.Fatal("plane refusal touched authority")
					}
					return AdapterOutput{}, fail(ProtocolMismatch, "synthetic-plane-refusal")
				case "expired_owner_deadline":
					deadline, e := time.Parse("2006-01-02T15:04:05.000Z", text(session.lease, "deadline"))
					if e != nil {
						t.Fatal(e)
					}
					clock.Advance(deadline)
					detail = "adapter_deadline"
				default:
					t.Fatal("unknown envelope case")
				}
				if name == "artifact" || name == "nonce" || name == "target_digest" || name == "epoch" || name == "deadline" {
					if equalJSON(normalize(session.lease), normalize(checkpoint["lease"])) {
						t.Fatal("lease mutation not installed in actual frame")
					}
				}
				validationErr := i.owner.registry.schemas.Validate(schemaPrefix+"adapter", normalize(checkpoint))
				if (validationErr == nil) != privateValid {
					t.Fatal("mutant did not reach planned validation layer", validationErr)
				}
				ack, out, err := session.Accept(ctx, rawJSON(t, checkpoint))
				if err == nil || ack != nil || out != nil {
					t.Fatal("invalid private envelope returned ACK/material")
				}
				if detail != "" {
					b2c1ErrorDetail(t, err, detail)
				}
				if sequence != session.inSeq || dispatch != i.snapshot.Journal[i.journalKey].Dispatches || calls != backend.calls() || !sameSnapshot(before, i.snapshot) || effects != 0 {
					t.Fatal("private refusal advanced state/storage/effect")
				}
				return AdapterOutput{}, err
			})
			o.now, o.deadlineHook = clock.Now, clock.Until
			backend = &b2c1ObservedBackend{Backend: b}
			o.backend = backend
			got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, fixtureRequest("perform"))))
			if reached != 1 {
				t.Fatal("validator fixture never reached")
			}
			if name == "valid_checkpoint" {
				if got["kind"] != "performed" || effects != 1 {
					t.Fatal("positive control failed", got["kind"])
				}
			} else if got["kind"] != "failed" || effects != 0 {
				t.Fatal("negative envelope performed")
			}
		})
	}
}
