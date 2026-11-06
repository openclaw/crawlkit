package sessionowner

import (
	"context"
	"encoding/json"
	"time"
)

// CandidateScope is private owner/adapter metadata, never a public selector.
type CandidateScope struct {
	BindingID, Caller, RequestID, OperationID, OperationDigest                                                          string
	OperationVersion                                                                                                    int
	AdapterID, AdapterArtifact, ClaimID, Epoch, LeaseID, InvocationID, ParentLifetimeID, Step, Direction, LeaseDeadline string
}
type CandidateMaterial struct {
	Raw       []byte
	Scope     CandidateScope
	ExpiresAt time.Time
}
type CandidateReference struct {
	Ref    string
	Bytes  int
	Digest string
}
type CandidateRepository interface {
	Read(context.Context, CandidateScope, string, int) (CandidateMaterial, error)
	Write(context.Context, CandidateScope, []byte, time.Time, int) (CandidateReference, error)
}
type CandidateBinding struct {
	Repository CandidateRepository
	// Mandatory for private inline AND staged inputs. Protected producer metadata
	// may qualify expiry; caller data or cookie presence never supplies a TTL.
	Expiry func(context.Context, CandidateScope, map[string]any, CandidateMaterial) (time.Time, error)
}
type CandidateTransfer struct {
	Delivery           *CandidateTransition `json:"delivery"`
	Retirement         *CandidateTransition `json:"retirement"`
	Key                string               `json:"key"`
	Scope              CandidateScope       `json:"scope"`
	Mode               string               `json:"mode"`
	Ref                string               `json:"ref"`
	Bytes              int                  `json:"bytes"`
	TransportDigest    string               `json:"transportDigest"`
	SemanticDigest     string               `json:"semanticDigest"`
	State              string               `json:"state"`
	AcceptanceCommitID string               `json:"acceptanceCommitId"`
	Candidate          map[string]any       `json:"candidate"`
	TransportRaw       []byte               `json:"transportRaw"`
	ExpiresAt          time.Time            `json:"expiresAt"`
	EffectIDs          []string             `json:"effectIds"`
}

func candidateScopeEqual(a, b CandidateScope) bool { return equalJSON(normalize(a), normalize(b)) }
func validCandidateScope(s CandidateScope) bool {
	for _, id := range []string{s.BindingID, s.Caller, s.RequestID, s.OperationID, s.AdapterID, s.ClaimID, s.Epoch, s.LeaseID, s.InvocationID, s.ParentLifetimeID} {
		if !validID(id) {
			return false
		}
	}
	return s.OperationVersion > 0 && s.OperationVersion <= 65535 && len(s.OperationDigest) == 64 && len(s.AdapterArtifact) == 64 && ValidTimestamp(s.LeaseDeadline) && (s.Direction == "adapter_to_authority" || s.Direction == "authority_to_adapter") && (s.Step == "acquire_candidate" || s.Step == "prove_candidate" || s.Step == "reconcile")
}

// StagingScope is for the fixed trusted producer/consumer binding, not a public
// operation or a credential accessor. No caller-selected path is returned.
func (s *AdapterSession) StagingScope(direction string) (CandidateScope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.candidateScope(direction)
}
func (s *AdapterSession) candidateScope(direction string) (CandidateScope, error) {
	i := s.invocation
	if i.claim == nil || !claimBound(i.snapshot, i.claim) {
		return CandidateScope{}, fail(PermissionDenied, "candidate_claim")
	}
	j := i.snapshot.Journal[i.journalKey]
	if j == nil {
		return CandidateScope{}, fail(PermissionDenied, "candidate_journal")
	}
	x := CandidateScope{BindingID: i.snapshot.BindingID, Caller: j.Caller, RequestID: j.RequestID, OperationID: i.request.Operation.ID, OperationVersion: i.request.Operation.Version, OperationDigest: i.request.Digest, AdapterID: text(s.lease, "adapterId"), AdapterArtifact: text(s.lease, "adapterArtifactSha256"), ClaimID: i.claim.ID, Epoch: i.snapshot.Generation, LeaseID: text(s.lease, "leaseId"), InvocationID: text(s.lease, "invocationId"), ParentLifetimeID: text(s.lease, "parentLifetimeId"), Step: i.step, Direction: direction, LeaseDeadline: text(s.lease, "deadline")}
	if !validCandidateScope(x) {
		return CandidateScope{}, fail(ProtocolMismatch, "candidate_scope")
	}
	return x, nil
}
func transferKey(scope CandidateScope, mode, ref string) string {
	if mode == "staged" {
		return scope.Direction + ":" + ref
	}
	return scope.Direction + ":inline:" + scope.LeaseID
}
func (o *Owner) candidateBinding(scope CandidateScope) (CandidateBinding, error) {
	b, ok := o.candidateBindings[scope.AdapterID]
	adapter, registered := o.registry.adapters[scope.AdapterID]
	if !ok || !registered || adapter.ArtifactSHA256 != scope.AdapterArtifact || b.Expiry == nil {
		return CandidateBinding{}, fail(ProtocolMismatch, "candidate_qualification")
	}
	return b, nil
}
func (o *Owner) resolveTransfer(ctx context.Context, i *Invocation, t *CandidateTransfer) (map[string]any, time.Time, error) {
	b, e := o.candidateBinding(t.Scope)
	if e != nil {
		return nil, time.Time{}, e
	}
	material := CandidateMaterial{Raw: append([]byte(nil), t.TransportRaw...), Scope: t.Scope}
	if t.Mode == "staged" {
		if b.Repository == nil {
			return nil, time.Time{}, fail(ProtocolMismatch, "candidate_resolver")
		}
		material, e = b.Repository.Read(ctx, t.Scope, t.Ref, i.request.Operation.WireCaps.MaxCandidateBytes)
		if e != nil {
			return nil, time.Time{}, e
		}
	}
	if ctx.Err() != nil || !candidateScopeEqual(material.Scope, t.Scope) || len(material.Raw) != t.Bytes || Hash(material.Raw) != t.TransportDigest || CheckBytes(material.Raw, i.request.Operation.WireCaps.MaxCandidateBytes) != nil {
		return nil, time.Time{}, fail(PermissionDenied, "candidate_transport")
	}
	v, e := DecodeJSON(material.Raw, i.request.Operation.WireCaps.MaxCandidateBytes)
	if e != nil {
		return nil, time.Time{}, e
	}
	value, ok := object(v)
	if !ok {
		return nil, time.Time{}, fail(ProtocolMismatch, "candidate_object")
	}
	adapter := o.registry.adapters[t.Scope.AdapterID]
	if adapter.CandidateSchemaID == nil || o.registry.schemas.Validate(*adapter.CandidateSchemaID, value) != nil {
		return nil, time.Time{}, fail(ProtocolMismatch, "candidate_schema")
	}
	expires, e := b.Expiry(ctx, t.Scope, cloneMap(value), material)
	if e != nil || expires.IsZero() || expires.Year() < 1 || expires.Year() > 9999 || !expires.After(o.now()) || t.Mode == "staged" && (material.ExpiresAt.IsZero() || expires.After(material.ExpiresAt)) {
		return nil, time.Time{}, fail(Code("rejected_generation"), "candidate_expiry")
	}
	if e = o.checkCandidateAdmission(i, value, expires); e != nil {
		return nil, time.Time{}, e
	}
	t.TransportRaw = append([]byte(nil), material.Raw...)
	return value, expires, nil
}
func (o *Owner) acceptTransfer(ctx context.Context, i *Invocation, t *CandidateTransfer) (*CandidateTransfer, error) {
	// Reservation already owns exact validated bytes and finite qualified expiry.
	// Reconciliation never re-reads a producer file or re-qualifies its lifetime.
	value, expires := t.Candidate, t.ExpiresAt
	if t.SemanticDigest != payloadDigest(value) || len(t.TransportRaw) != t.Bytes || Hash(t.TransportRaw) != t.TransportDigest {
		return nil, fail(ReconciliationRequired, "candidate_retained_material")
	}
	if e := o.checkCandidateAdmission(i, value, expires); e != nil {
		return nil, e
	}
	if ctx.Err() != nil || i.active.channelRef.check(o) != nil {
		return nil, fail(Canceled, "candidate_lifetime")
	}
	before := i.snapshot
	tcopy := *t
	tcopy.State = "accepted"
	if t.Scope.Direction == "authority_to_adapter" {
		tcopy.State = "issued"
	}
	tcopy.Candidate = cloneMap(value)
	tcopy.SemanticDigest = payloadDigest(value)
	tcopy.ExpiresAt = expires
	desired, e := before.Clone()
	if e != nil {
		return nil, e
	}
	desired.CandidateTransfers[t.Key] = &tcopy
	loaded, e := o.saveWithID(ctx, i.guard, desired, t.AcceptanceCommitID)
	if e != nil {
		return nil, e
	}
	i.installSnapshot(loaded)
	if ctx.Err() != nil || i.active.channelRef.check(o) != nil {
		return nil, fail(Canceled, "candidate_lifetime")
	}
	return i.snapshot.CandidateTransfers[t.Key], nil
}
func (s *AdapterSession) receiveCandidate(ctx context.Context, wire map[string]any) (*CandidateTransfer, error) {
	i := s.invocation
	if !s.permitted.Load() || (i.step != "acquire_candidate" && i.step != "reconcile") {
		return nil, fail(PermissionDenied, "candidate_step")
	}
	scope, e := s.candidateScope("adapter_to_authority")
	if e != nil {
		return nil, e
	}
	if _, e = i.owner.candidateBinding(scope); e != nil {
		return nil, e
	}
	mode := text(wire, "mode")
	t := &CandidateTransfer{Scope: scope, Mode: mode, Ref: text(wire, "candidateRef"), State: "reserved", AcceptanceCommitID: newID(), EffectIDs: append([]string(nil), i.claim.EffectIDs...)}
	if mode == "inline" {
		t.TransportRaw, e = json.Marshal(wire["value"])
		if e != nil {
			return nil, fail(ProtocolMismatch, "candidate_inline")
		}
		t.Bytes = len(t.TransportRaw)
		t.TransportDigest = Hash(t.TransportRaw)
	} else if mode == "staged" {
		t.Bytes = int(num(wire["bytes"]))
		t.TransportDigest = text(wire, "privateDigest")
	} else {
		return nil, fail(ProtocolMismatch, "candidate_mode")
	}
	if t.Bytes < 1 || t.Bytes > i.request.Operation.WireCaps.MaxCandidateBytes {
		return nil, fail(PayloadTooLarge, "candidate_bytes")
	}
	t.Key = transferKey(scope, mode, t.Ref)
	if i.snapshot.CandidateTransfers == nil {
		i.snapshot.CandidateTransfers = map[string]*CandidateTransfer{}
	}
	if _, exists := i.snapshot.CandidateTransfers[t.Key]; exists {
		return nil, fail(PermissionDenied, "candidate_replay")
	}
	if len(i.snapshot.CandidateTransfers) >= 4096 {
		return nil, fail(StorageFailure, "candidate_capacity")
	}
	value, expires, e := i.owner.resolveTransfer(ctx, i, t)
	if e != nil {
		return nil, e
	}
	t.Candidate = cloneMap(value)
	t.SemanticDigest = payloadDigest(value)
	t.ExpiresAt = expires
	i.snapshot.CandidateTransfers[t.Key] = t
	loaded, e := i.owner.save(ctx, i.guard, i.snapshot)
	if e != nil {
		return nil, e
	}
	i.installSnapshot(loaded)
	t = i.snapshot.CandidateTransfers[t.Key]
	return i.owner.acceptTransfer(ctx, i, t)
}

func validateCandidateTransfers(s *Snapshot) error {
	for key, t := range s.CandidateTransfers {
		if t == nil || t.Key != key || !validCandidateScope(t.Scope) || t.Scope.BindingID != s.BindingID || t.Bytes < 1 || t.Bytes > HardFrameLimit || len(t.TransportDigest) != 64 || !validID(t.AcceptanceCommitID) {
			return fail(ReconciliationRequired, "candidate_transfer_state")
		}
		if e := validateCandidateTransitions(s, t); e != nil {
			return e
		}
		c := s.Claims[t.Scope.ClaimID]
		j := s.Journal[t.Scope.Caller+"\x00"+t.Scope.RequestID]
		if !claimBound(s, c) || c.Caller != t.Scope.Caller || c.Epoch != t.Scope.Epoch || j == nil || j.ClaimID != c.ID || j.Digest != t.Scope.OperationDigest || j.OperationID != t.Scope.OperationID || j.OperationVersion != t.Scope.OperationVersion {
			return fail(ReconciliationRequired, "candidate_transfer_binding")
		}
		if t.Mode == "staged" && !validID(t.Ref) {
			return fail(ReconciliationRequired, "candidate_transfer_ref")
		}
		if len(t.TransportRaw) != t.Bytes || Hash(t.TransportRaw) != t.TransportDigest {
			return fail(ReconciliationRequired, "candidate_transfer_transport")
		}
		if key != transferKey(t.Scope, t.Mode, t.Ref) || (t.Mode != "inline" && t.Mode != "staged") {
			return fail(ReconciliationRequired, "candidate_transfer_key")
		}
		if t.State == "accepted" && t.Scope.Direction != "adapter_to_authority" || (t.State == "issued" || t.State == "delivered" || t.State == "retired") && t.Scope.Direction != "authority_to_adapter" {
			return fail(ReconciliationRequired, "candidate_transfer_direction")
		}
		decoded, e := DecodeJSON(t.TransportRaw, HardFrameLimit)
		if e != nil || !equalJSON(decoded, normalize(t.Candidate)) {
			return fail(ReconciliationRequired, "candidate_transfer_encoding")
		}
		switch t.State {
		case "reserved", "accepted", "issued", "delivered", "retired":
			if t.Candidate == nil || t.SemanticDigest != payloadDigest(t.Candidate) || t.ExpiresAt.IsZero() {
				return fail(ReconciliationRequired, "candidate_transfer_payload")
			}
		default:
			return fail(ReconciliationRequired, "candidate_transfer_phase")
		}
	}
	for _, c := range s.Claims {
		if c != nil && c.CandidateTransferKey != "" {
			t := s.CandidateTransfers[c.CandidateTransferKey]
			if t == nil || t.State != "accepted" || t.Scope.ClaimID != c.ID || t.SemanticDigest != c.CandidateDigest || !t.ExpiresAt.Equal(c.CandidateExpires) {
				return fail(ReconciliationRequired, "claim_transfer_binding")
			}
		}
	}
	return nil
}

func (s *AdapterSession) outboundCandidate(ctx context.Context, v map[string]any) ([]byte, error) {
	i := s.invocation
	scope, e := s.candidateScope("authority_to_adapter")
	if e != nil {
		return nil, e
	}
	b, e := i.owner.candidateBinding(scope)
	if e != nil || b.Repository == nil {
		return nil, fail(ProtocolMismatch, "candidate_export_binding")
	}
	if i.claim == nil || i.Candidate == nil || !equalJSON(normalize(i.Candidate), normalize(i.claim.Candidate)) {
		return nil, fail(ReconciliationRequired, "candidate_export_scope")
	}
	if e = i.owner.checkCandidateAdmission(i, i.Candidate, i.claim.CandidateExpires); e != nil {
		return nil, e
	}
	raw, e := json.Marshal(i.Candidate)
	if e != nil || CheckBytes(raw, i.request.Operation.WireCaps.MaxCandidateBytes) != nil {
		return nil, fail(PayloadTooLarge, "candidate_export")
	}
	// Prove the descriptor representation fits before publishing a staging file.
	v["candidate"] = map[string]any{"mode": "staged", "candidateRef": "00000000000000000000000000000000", "bytes": len(raw), "privateDigest": Hash(raw)}
	if _, e = s.encode(v); e != nil {
		return nil, e
	}
	ref, e := b.Repository.Write(ctx, scope, raw, i.claim.CandidateExpires, i.request.Operation.WireCaps.MaxCandidateBytes)
	if e != nil {
		return nil, e
	}
	if !validID(ref.Ref) || ref.Bytes != len(raw) || ref.Digest != Hash(raw) {
		return nil, fail(ProtocolMismatch, "candidate_export_receipt")
	}
	t := &CandidateTransfer{Scope: scope, Mode: "staged", Ref: ref.Ref, Bytes: ref.Bytes, TransportDigest: ref.Digest, SemanticDigest: i.claim.CandidateDigest, State: "reserved", AcceptanceCommitID: newID(), Candidate: cloneMap(i.Candidate), TransportRaw: append([]byte(nil), raw...), ExpiresAt: i.claim.CandidateExpires}
	t.Key = transferKey(scope, t.Mode, t.Ref)
	desired, e := i.snapshot.Clone()
	if e != nil {
		return nil, e
	}
	if desired.CandidateTransfers == nil {
		desired.CandidateTransfers = map[string]*CandidateTransfer{}
	}
	if len(desired.CandidateTransfers) >= 4096 {
		return nil, fail(StorageFailure, "candidate_capacity")
	}
	if _, exists := desired.CandidateTransfers[t.Key]; exists {
		return nil, fail(PermissionDenied, "candidate_export_replay")
	}
	desired.CandidateTransfers[t.Key] = t
	loaded, e := i.owner.save(ctx, i.guard, desired)
	if e != nil {
		return nil, e
	}
	i.installSnapshot(loaded)
	if _, e = i.owner.acceptTransfer(ctx, i, i.snapshot.CandidateTransfers[t.Key]); e != nil {
		return nil, e
	}
	if ctx.Err() != nil || i.active.channelRef.check(i.owner) != nil {
		return nil, fail(Canceled, "candidate_export_lifetime")
	}
	s.outboundKey = t.Key
	v["candidate"] = map[string]any{"mode": "staged", "candidateRef": ref.Ref, "bytes": ref.Bytes, "privateDigest": ref.Digest}
	return s.encode(v)
}

// Local recovery is authorized by the existing same-claim resume. An expired old
// transport lease is never revived; only its retained immutable candidate record
// may be reconciled under the NEW admitted operation deadline.
func (o *Owner) restoreCandidateTransfer(ctx context.Context, i *Invocation) error {
	if e := o.retireOutboundTransfers(ctx, i); e != nil {
		return e
	}
	if i.claim.Candidate != nil {
		return nil
	}
	var found *CandidateTransfer
	for _, t := range i.snapshot.CandidateTransfers {
		if t.Scope.ClaimID == i.claim.ID && t.Scope.Direction == "adapter_to_authority" && (t.Scope.Step == "acquire_candidate" || t.Scope.Step == "reconcile") {
			if found != nil {
				return fail(ReconciliationRequired, "multiple_candidate_transfers")
			}
			found = t
		}
	}
	if found == nil {
		return nil
	}
	if found.Scope.Caller != i.claim.Caller || found.Scope.Epoch != i.claim.Epoch {
		return fail(PermissionDenied, "candidate_resume_scope")
	}
	if _, e := o.candidateBinding(found.Scope); e != nil {
		return e
	}
	r, e := o.backend.Reconcile(ctx, i.guard, found.AcceptanceCommitID)
	if e != nil || r.OperationID != found.AcceptanceCommitID {
		return fail(ReconciliationRequired, "candidate_transfer_readback")
	}
	if r.Certainty == "committed" {
		loaded, e := o.backend.Load(ctx, i.guard)
		if e != nil {
			return e
		}
		record := loaded.CandidateTransfers[found.Key]
		stored, ok := loaded.Commits[found.AcceptanceCommitID]
		if record == nil || record.State != "accepted" || !ok || !receiptMatches(r, stored) || !candidateScopeEqual(record.Scope, found.Scope) {
			return fail(ReconciliationRequired, "candidate_transfer_commit")
		}
		i.installSnapshot(loaded)
		found = record
	} else if r.Certainty == "not_committed" && found.State == "reserved" {
		accepted, e := o.acceptTransfer(ctx, i, found)
		if e != nil {
			return e
		}
		found = accepted
	} else {
		return fail(ReconciliationRequired, "candidate_transfer_uncertain")
	}
	if found.SemanticDigest != payloadDigest(found.Candidate) || o.checkCandidateAdmission(i, found.Candidate, found.ExpiresAt) != nil {
		return fail(Code("rejected_generation"), "candidate_transfer_admission")
	}
	if i.claim.Phase != "ACQUIRING" && i.claim.Phase != "EFFECT_UNCERTAIN" {
		return fail(ReconciliationRequired, "candidate_transfer_phase")
	}
	if !equalJSON(normalize(found.EffectIDs), normalize(i.claim.EffectIDs)) {
		return fail(ReconciliationRequired, "candidate_transfer_effect")
	}
	i.claim.Candidate = cloneMap(found.Candidate)
	i.claim.CandidateDigest = found.SemanticDigest
	i.claim.CandidateExpires = found.ExpiresAt
	i.claim.CandidateTransferKey = found.Key
	i.claim.Phase = "CANDIDATE_READY"
	resolveClaimJournals(i.snapshot, i.claim, newID())
	i.claim.ActiveStep = ""
	i.claim.ActiveJournalKey = ""
	i.claim.EffectIDs = nil
	i.claim.NoEffectProved = true
	return o.persistClaim(ctx, i)
}

// ConsumeStagedCandidate is the trusted local handoff used by an installed
// private adapter bridge. Repository.Read alone is not a use authorization.
// It reuses this invocation's existing backend guard, never reacquiring it.
func (s *AdapterSession) ConsumeStagedCandidate(caller context.Context, ref string, declaredBytes int, digest string) (material CandidateMaterial, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.invocation
	if s.parentContext.Err() != nil || s.terminal || s.permitted.Load() {
		return CandidateMaterial{}, fail(Canceled, "candidate_consumer_lifetime")
	}
	if s.consumeAttempted {
		return CandidateMaterial{}, fail(PermissionDenied, "candidate_consumer_replay")
	}
	scope, e := s.candidateScope("authority_to_adapter")
	if e != nil {
		return CandidateMaterial{}, e
	}
	key := transferKey(scope, "staged", ref)
	tr := i.snapshot.CandidateTransfers[key]
	if tr == nil || tr.State != "issued" || tr.Delivery != nil || tr.Retirement != nil || s.outboundKey != key || !candidateScopeEqual(tr.Scope, scope) || tr.Bytes != declaredBytes || tr.TransportDigest != digest {
		return CandidateMaterial{}, fail(PermissionDenied, "candidate_consumer_scope")
	}
	s.consumeAttempted = true
	defer func() {
		if err != nil {
			s.terminal = true
		}
	}()
	ctx, stop, check, e := s.candidateHandoffContext(caller, tr.ExpiresAt)
	if e != nil {
		return CandidateMaterial{}, e
	}
	defer stop()
	binding, e := i.owner.candidateBinding(scope)
	if e != nil || binding.Repository == nil {
		return CandidateMaterial{}, fail(ProtocolMismatch, "candidate_consumer_binding")
	}
	m, e := binding.Repository.Read(ctx, scope, ref, i.request.Operation.WireCaps.MaxCandidateBytes)
	if e != nil {
		return CandidateMaterial{}, e
	}
	if e = check(); e != nil {
		return CandidateMaterial{}, e
	}
	if !candidateScopeEqual(m.Scope, scope) || len(m.Raw) != tr.Bytes || Hash(m.Raw) != tr.TransportDigest || !m.ExpiresAt.Equal(tr.ExpiresAt) {
		return CandidateMaterial{}, fail(PermissionDenied, "candidate_consumer_bytes")
	}
	decoded, e := DecodeJSON(m.Raw, i.request.Operation.WireCaps.MaxCandidateBytes)
	if e != nil || payloadDigest(decoded) != tr.SemanticDigest || !equalJSON(decoded, normalize(i.Candidate)) {
		return CandidateMaterial{}, fail(PermissionDenied, "candidate_consumer_semantics")
	}
	if e = i.owner.checkCandidateAdmission(i, tr.Candidate, tr.ExpiresAt); e != nil {
		return CandidateMaterial{}, e
	}
	// An exact delivery ID exists durably before any transaction can mark the
	// bytes delivered. A failed later marker cannot erase this preparation.
	desired, e := i.snapshot.Clone()
	if e != nil {
		return CandidateMaterial{}, e
	}
	desired.CandidateTransfers[key].Delivery = newCandidateTransition(i, "issued", "delivered")
	if e = check(); e != nil {
		return CandidateMaterial{}, e
	}
	loaded, e := i.owner.save(ctx, i.guard, desired)
	if e != nil {
		return CandidateMaterial{}, e
	}
	i.installSnapshot(loaded)
	if e = check(); e != nil {
		return CandidateMaterial{}, e
	}
	desired, e = i.snapshot.Clone()
	if e != nil {
		return CandidateMaterial{}, e
	}
	next := desired.CandidateTransfers[key]
	next.State = "delivered"
	next.Delivery.State = "committed"
	if e = check(); e != nil {
		return CandidateMaterial{}, e
	}
	loaded, e = i.owner.saveWithID(ctx, i.guard, desired, next.Delivery.ID)
	if e != nil {
		return CandidateMaterial{}, e
	}
	i.installSnapshot(loaded)
	if e = check(); e != nil {
		return CandidateMaterial{}, e
	}
	s.deliveryReady = true
	return m, nil
}
