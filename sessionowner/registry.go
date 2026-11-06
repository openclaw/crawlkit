package sessionowner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type WireCaps struct {
	RequestFrameBytes       int `json:"requestFrameBytes"`
	ResponseFrameBytes      int `json:"responseFrameBytes"`
	AdapterFrameBytes       int `json:"adapterFrameBytes"`
	MaxCandidateBytes       int `json:"maxCandidateBytes"`
	MaxPrivateContextBytes  int `json:"maxPrivateContextBytes"`
	MaxCredentialDeltaBytes int `json:"maxCredentialDeltaBytes"`
	MaxProviderBodyBytes    int `json:"maxProviderBodyBytes"`
	MaxCursorBytes          int `json:"maxCursorBytes"`
}
type BudgetPlan struct {
	ID                         string           `json:"planId"`
	Readiness                  string           `json:"readiness"`
	PhaseMaxMs                 map[string]int64 `json:"phaseMaxMs"`
	MaxTotalMs                 int64            `json:"maxTotalMs"`
	MaxRPCMs                   *int64           `json:"maxRpcMs"`
	MaxSingleProviderRequestMs *int64           `json:"maxSingleProviderRequestMs"`
	MaxProviderRequests        int              `json:"maxProviderRequests"`
	MaxProviderBytes           int64            `json:"maxProviderBytes"`
	MaxSafeReadRetries         int              `json:"maxSafeReadRetries"`
	MaxAcquisitionAttempts     int              `json:"maxAcquisitionAttempts"`
	MaxReadinessResumptions    int              `json:"maxReadinessResumptions"`
	MaxProofAttempts           int              `json:"maxProofAttempts"`
}
type Operation struct {
	ID                    string       `json:"operationId"`
	Version               int          `json:"operationVersion"`
	RequestKind           string       `json:"requestKind"`
	Semantics             string       `json:"semantics"`
	AdapterID             *string      `json:"adapterId"`
	Transport             string       `json:"transport"`
	TransportDescriptorID *string      `json:"transportDescriptorId"`
	TargetSchemaID        *string      `json:"targetSchemaId"`
	ArgsSchemaID          *string      `json:"argsSchemaId"`
	ResultSchemaID        *string      `json:"resultSchemaId"`
	ConfirmationRequired  bool         `json:"confirmationRequired"`
	CursorPolicy          string       `json:"cursorPolicy"`
	IdentityDeltaPolicyID *string      `json:"identityDeltaPolicyId"`
	MinimumRetryDelayMs   int64        `json:"minimumRetryDelayMs"`
	WireCaps              WireCaps     `json:"wireCaps"`
	BudgetPlans           []BudgetPlan `json:"budgetPlans"`
}
type SchemaBinding struct {
	ID     string `json:"schemaId"`
	SHA256 string `json:"sha256"`
	Slot   string `json:"slot"`
	Closed bool   `json:"closed"`
}
type AdapterBinding struct {
	ID                         string  `json:"adapterId"`
	ArtifactSHA256             string  `json:"artifactSha256"`
	FixedEntryID               string  `json:"fixedEntryId"`
	PrivateContextSchemaID     string  `json:"privateContextSchemaId"`
	CandidateSchemaID          *string `json:"candidateSchemaId"`
	CredentialDeltaSchemaID    *string `json:"credentialDeltaSchemaId"`
	ProofSchemaID              *string `json:"proofSchemaId"`
	CheckpointEvidenceSchemaID string  `json:"checkpointEvidenceSchemaId"`
	SDKArtifactSHA256          *string `json:"sdkArtifactSha256"`
}
type Policy struct {
	Version             string            `json:"policyVersion"`
	ID                  string            `json:"registryId"`
	Revision            string            `json:"registryRevision"`
	OwnerArtifactSHA256 string            `json:"ownerArtifactSha256"`
	BindingID           string            `json:"bindingId"`
	AuthorityMode       string            `json:"authorityMode"`
	SafeIdentity        map[string]string `json:"safeIdentity"`
	Schemas             []SchemaBinding   `json:"schemas"`
	Adapters            []AdapterBinding  `json:"adapters"`
	Operations          []Operation       `json:"operations"`
	StorageBackend      map[string]any    `json:"storageBackend"`
	DetailCodes         []string          `json:"detailCodes"`
}

// Registry is immutable after construction. Raw policy and schemas are pinned by
// caller-independent owner artifact hashes. Maps returned by callers are not kept.
type Registry struct {
	policy     Policy
	schemas    *SchemaStore
	operations map[string]Operation
	builtins   map[string]Operation
	adapters   map[string]AdapterBinding
	slots      map[string]string
}

func NewRegistry(raw []byte, expectedSHA256 string, artifacts map[string][]byte) (*Registry, error) {
	if Hash(raw) != expectedSHA256 {
		return nil, fail(ProtocolMismatch, "registry_hash")
	}
	s, e := NewSchemaStore()
	if e != nil {
		return nil, e
	}
	v, e := DecodeJSON(raw, HardFrameLimit)
	if e != nil {
		return nil, e
	}
	if e = s.Validate(schemaPrefix+"registry-policy", v); e != nil {
		return nil, e
	}
	r := &Registry{schemas: s, operations: map[string]Operation{}, builtins: map[string]Operation{}, adapters: map[string]AdapterBinding{}, slots: map[string]string{}}
	if e = json.Unmarshal(raw, &r.policy); e != nil {
		return nil, fail(ProtocolMismatch, "policy")
	}
	for _, b := range r.policy.Schemas {
		if _, ok := r.slots[b.ID]; ok {
			return nil, fail(ProtocolMismatch, "schema_duplicate")
		}
		bytes, ok := artifacts[b.ID]
		if !ok || Hash(bytes) != b.SHA256 {
			return nil, fail(ProtocolMismatch, "schema_hash")
		}
		if e = s.Register(b.ID, bytes); e != nil {
			return nil, e
		}
		r.slots[b.ID] = b.Slot
	}
	for _, a := range r.policy.Adapters {
		if _, ok := r.adapters[a.ID]; ok {
			return nil, fail(ProtocolMismatch, "adapter_duplicate")
		}
		if r.slots[a.PrivateContextSchemaID] != "private_context" || r.slots[a.CheckpointEvidenceSchemaID] != "checkpoint_evidence" {
			return nil, fail(ProtocolMismatch, "adapter_schema")
		}
		for slot, id := range map[string]*string{"candidate": a.CandidateSchemaID, "credential_delta": a.CredentialDeltaSchemaID, "proof": a.ProofSchemaID} {
			if id != nil && r.slots[*id] != slot {
				return nil, fail(ProtocolMismatch, "adapter_schema")
			}
		}
		r.adapters[a.ID] = a
	}
	for _, op := range r.policy.Operations {
		key := operationKey(op.ID, op.Version, op.RequestKind)
		if _, ok := r.operations[key]; ok {
			return nil, fail(ProtocolMismatch, "operation_duplicate")
		}
		if op.AdapterID != nil {
			if _, ok := r.adapters[*op.AdapterID]; !ok {
				return nil, fail(ProtocolMismatch, "adapter_missing")
			}
		}
		for slot, id := range map[string]*string{"target": op.TargetSchemaID, "args": op.ArgsSchemaID, "result": op.ResultSchemaID} {
			if id != nil && r.slots[*id] != slot {
				return nil, fail(ProtocolMismatch, "operation_schema")
			}
		}
		plans := map[string]bool{}
		for _, b := range op.BudgetPlans {
			if plans[b.ID] {
				return nil, fail(ProtocolMismatch, "plan_duplicate")
			}
			plans[b.ID] = true
			if op.Semantics != "safe_read" && b.MaxSafeReadRetries != 0 {
				return nil, fail(ProtocolMismatch, "retry_policy")
			}
		}
		if op.RequestKind != "perform" {
			if _, ok := r.builtins[op.RequestKind]; ok {
				return nil, fail(ProtocolMismatch, "builtin_duplicate")
			}
			r.builtins[op.RequestKind] = op
		}
		r.operations[key] = op
	}
	return r, nil
}
func Hash(b []byte) string                           { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func operationKey(id string, v int, k string) string { return fmt.Sprintf("%s/%d/%s", id, v, k) }
func (r *Registry) BindingID() string                { return r.policy.BindingID }
func (r *Registry) ChannelCap() int {
	n := 0
	for _, o := range r.operations {
		if o.WireCaps.RequestFrameBytes > n {
			n = o.WireCaps.RequestFrameBytes
		}
	}
	return n
}
func (r *Registry) SchemaStore() *SchemaStore { return r.schemas }

type Request struct {
	acceptedGeneration string
	Value              map[string]any
	Operation          Operation
	Deadline           time.Time
	Digest             string
}

func (r *Registry) ParseRequest(raw []byte) (*Request, error) {
	v, e := DecodeJSON(raw, r.ChannelCap())
	if e != nil {
		return nil, e
	}
	m, ok := object(v)
	if !ok {
		return nil, fail(ProtocolMismatch, "request_object")
	}
	name := "request"
	if text(m, "kind") == "cancel" {
		name = "cancel"
	}
	if e = r.schemas.Validate(schemaPrefix+name, v); e != nil {
		return nil, e
	}
	if text(m, "bindingId") != r.policy.BindingID {
		return nil, fail(PermissionDenied, "binding")
	}
	k := text(m, "kind")
	var op Operation
	if k == "perform" {
		op, ok = r.operations[operationKey(text(m, "operationId"), int(num(m["operationVersion"])), k)]
	} else {
		if k == "recover" {
			k += "_" + text(m, "mode")
		}
		op, ok = r.builtins[k]
	}
	if !ok {
		return nil, fail(Code("unsupported_operation"), "operation")
	}
	op = cloneOperation(op)
	if len(raw) > op.WireCaps.RequestFrameBytes {
		return nil, fail(PayloadTooLarge, "request")
	}
	if op.RequestKind == "perform" {
		if e = r.schemas.Validate(*op.TargetSchemaID, m["target"]); e != nil {
			return nil, e
		}
		if e = r.schemas.Validate(*op.ArgsSchemaID, m["args"]); e != nil {
			return nil, e
		}
		if op.ConfirmationRequired && m["confirmationRef"] == nil {
			return nil, fail(PermissionDenied, "confirmation")
		}
		if !op.ConfirmationRequired && m["confirmationRef"] != nil {
			return nil, fail(ProtocolMismatch, "confirmation")
		}
	}
	t, _ := time.Parse("2006-01-02T15:04:05.000Z", text(m, "deadline"))
	// Deadline and request ID are correlation, not business identity. A retry cannot
	// change its target/args/authority; a new deadline never revives old effects.
	digestMap := map[string]any{}
	for k, v := range m {
		if k != "deadline" {
			digestMap[k] = v
		}
	}
	b, _ := CanonicalJSON(digestMap)
	return &Request{Value: m, Operation: op, Deadline: t, Digest: Hash(b)}, nil
}
func (r *Registry) ValidateResponse(b []byte, req *Request) error {
	if len(b) > req.Operation.WireCaps.ResponseFrameBytes {
		return fail(PayloadTooLarge, "response")
	}
	v, e := DecodeJSON(b, req.Operation.WireCaps.ResponseFrameBytes)
	if e != nil {
		return e
	}
	if e = r.schemas.Validate(schemaPrefix+"response", v); e != nil {
		return e
	}
	m, _ := object(v)
	if text(m, "requestId") != text(req.Value, "requestId") || text(m, "bindingId") != r.BindingID() {
		return fail(ProtocolMismatch, "response_correlation")
	}
	switch text(m, "kind") {
	case "performed":
		if req.Operation.RequestKind != "perform" || text(m, "operationId") != req.Operation.ID || int(num(m["operationVersion"])) != req.Operation.Version {
			return fail(ProtocolMismatch, "response_operation")
		}
		return r.schemas.Validate(*req.Operation.ResultSchemaID, m["result"])
	case "failed":
		f, _ := object(m["failure"])
		for _, key := range []string{"certainty", "nextAction", "retryNotBefore"} {
			if !equalJSON(f[key], m[key]) {
				return fail(ProtocolMismatch, "failure_correlation")
			}
		}
		if f["detailCode"] != nil {
			found := false
			for _, c := range r.policy.DetailCodes {
				found = found || c == text(f, "detailCode")
			}
			if !found {
				return fail(ProtocolMismatch, "detail_code")
			}
		}
		if text(m, "requestKind") != text(req.Value, "kind") {
			return fail(ProtocolMismatch, "failure_kind")
		}
	case "inspection":
		if req.Operation.RequestKind != "inspect" {
			return fail(ProtocolMismatch, "response_kind")
		}
		if text(m, "state") == "unenrolled" && (m["generation"] != nil || m["stateRevision"] != nil || m["safeIdentity"] != nil) {
			return fail(ProtocolMismatch, "unknown_state")
		}
		if text(m, "state") == "ready" && (m["generation"] == nil || m["stateRevision"] == nil || m["safeIdentity"] == nil) {
			return fail(ProtocolMismatch, "unknown_state")
		}
	case "recovered":
		if text(req.Value, "kind") != "recover" {
			return fail(ProtocolMismatch, "response_kind")
		}
	case "cancel_ack":
		if text(req.Value, "kind") != "cancel" || text(m, "targetRequestId") != text(req.Value, "targetRequestId") {
			return fail(ProtocolMismatch, "cancel_correlation")
		}
	default:
		return fail(ProtocolMismatch, "unexpected_response")
	}
	return nil
}

// ReservePlan is called by the OWNER with a proven readiness condition, never a
// caller-selected plan. Unavailable warm readiness does not permit cold fallback.
func ReservePlan(req *Request, now time.Time, warm bool, remaining []string) (BudgetPlan, error) {
	for _, p := range req.Operation.BudgetPlans {
		if p.Readiness == "warm_only" && !warm {
			continue
		}
		var required int64
		seen := map[string]bool{}
		for _, phase := range remaining {
			if seen[phase] {
				return BudgetPlan{}, fail(ProtocolMismatch, "phase_duplicate")
			}
			seen[phase] = true
			n, ok := p.PhaseMaxMs[phase]
			if !ok {
				return BudgetPlan{}, fail(ProtocolMismatch, "phase")
			}
			if phase == "startup" && warm {
				continue
			}
			required += phaseReserve(phase, n)
		}
		if !seen["cleanup"] {
			required += p.PhaseMaxMs["cleanup"]
		}
		available := req.Deadline.Sub(now)
		if required <= p.MaxTotalMs && available >= time.Duration(required)*time.Millisecond && available > 0 {
			return p, nil
		}
	}
	return BudgetPlan{}, fail(BudgetInsufficient, "phase_reserve")
}
func CheckBytes(b []byte, max int) error {
	if max < 0 || len(b) > max {
		return fail(PayloadTooLarge, "slot")
	}
	return nil
}

// The action value is a ceiling, not a mandatory allocation of the entire
// native operation window. Work gets a context clamped to its remaining window.
// Startup/capture/proof reserves remain the qualified required worst-case phases.
func phaseReserve(phase string, maxMs int64) int64 {
	if phase == "action" && maxMs > 0 {
		return 1
	}
	return maxMs
}

func cloneOperation(op Operation) Operation {
	b, _ := json.Marshal(op)
	var copied Operation
	_ = json.Unmarshal(b, &copied)
	return copied
}
