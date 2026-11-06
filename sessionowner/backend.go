package sessionowner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"sync"
	"time"
)

// Guard is intentionally not serializable. Native storage bindings may wrap an
// existing lock using NewGuard; callers on the public wire can never supply it.
type Guard struct {
	mu      sync.Mutex
	owner   any
	release func() error
	verify  func() error
	closed  bool
}

func NewGuard(owner any, release func() error) *Guard { return &Guard{owner: owner, release: release} }
func (g *Guard) Check(owner any) error {
	if g == nil {
		return fail(UnsafeStorage, "guard")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.owner == nil || g.owner != owner {
		return fail(UnsafeStorage, "guard")
	}
	if g.verify != nil {
		return g.verify()
	}
	return nil
}
func (g *Guard) Close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	if g.release != nil {
		return g.release()
	}
	return nil
}

type Expected struct {
	Generation    string
	StateRevision uint64
	ClaimVersion  uint64
}
type CommitReceipt struct {
	SettlementGraph    string `json:"settlementGraph,omitempty"`
	SettlementProposal string `json:"settlementProposal,omitempty"`
	OperationID        string `json:"operationId"`
	Certainty          string `json:"certainty"`
	Generation         string `json:"generation"`
	StateRevision      uint64 `json:"stateRevision,string"`
	Digest             string `json:"digest"`
}
type Backend interface {
	Acquire(context.Context) (*Guard, error)
	Load(context.Context, *Guard) (*Snapshot, error)
	Commit(context.Context, *Guard, Expected, *Snapshot, string) (CommitReceipt, error)
	Reconcile(context.Context, *Guard, string) (CommitReceipt, error)
}
type IntentReservation struct {
	BeforeDispatches          int    `json:"beforeDispatches"`
	BeforeAcquisitionAttempts int    `json:"beforeAcquisitionAttempts"`
	BeforeProofAttempts       int    `json:"beforeProofAttempts"`
	ID                        string `json:"id"`
	State                     string `json:"state"`
	BindingID                 string `json:"bindingId"`
	Caller                    string `json:"caller"`
	RequestID                 string `json:"requestId"`
	RequestDigest             string `json:"requestDigest"`
	OperationID               string `json:"operationId"`
	OperationVersion          int    `json:"operationVersion"`
	Epoch                     string `json:"epoch"`
	ClaimID                   string `json:"claimId"`
	Step                      string `json:"step"`
	Phase                     string `json:"phase"`
	EffectID                  string `json:"effectId"`
}

type JournalEntry struct {
	ChildAdmittedAt       time.Time                    `json:"childAdmittedAt"`
	ChildMaxTotalMs       int64                        `json:"childMaxTotalMs"`
	CompositionBodyCap    int64                        `json:"compositionBodyCap"`
	ResponseCommitID      string                       `json:"responseCommitId"`
	PendingResponse       []byte                       `json:"pendingResponse"`
	Composition           *ReadComposition             `json:"composition"`
	CompositionKey        string                       `json:"compositionKey"`
	ProviderBytesReserved int64                        `json:"providerBytesReserved"`
	Intents               map[string]IntentReservation `json:"intents"`
	UnresolvedIntentID    string                       `json:"unresolvedIntentId"`
	Generation            string                       `json:"generation"`
	OperationID           string                       `json:"operationId"`
	OperationVersion      int                          `json:"operationVersion"`
	RequestKind           string                       `json:"requestKind"`
	ClaimID               string                       `json:"claimId"`
	EffectIDs             []string                     `json:"effectIds"`
	ResolutionID          string                       `json:"resolutionId"`
	Diagnosis             *RejectedDiagnosis           `json:"diagnosis"`

	Caller     string          `json:"caller"`
	RequestID  string          `json:"requestId"`
	Digest     string          `json:"digest"`
	Semantics  string          `json:"semantics"`
	Certainty  string          `json:"certainty"`
	Phase      string          `json:"phase"`
	ReceiptID  string          `json:"receiptId"`
	Response   json.RawMessage `json:"response"`
	Deadline   time.Time       `json:"deadline"`
	Dispatches int             `json:"dispatches"`
}
type Snapshot struct {
	settlementVerified map[string]settlementVerification
	CandidateTransfers map[string]*CandidateTransfer `json:"candidateTransfers"`
	Version            string                        `json:"version"`
	BindingID          string                        `json:"bindingId"`
	Generation         string                        `json:"generation"`
	StateRevision      uint64                        `json:"stateRevision,string"`
	CredentialRevision uint64                        `json:"credentialRevision,string"`
	ClaimVersion       uint64                        `json:"claimVersion,string"`
	Credentials        map[string]any                `json:"credentials"`
	Identity           map[string]any                `json:"identity"`
	Provenance         map[string]any                `json:"provenance"`
	AuthBlocked        bool                          `json:"authBlocked"`
	Maintenance        bool                          `json:"maintenance"`
	Fences             map[string]time.Time          `json:"fences"`
	Claims             map[string]*Claim             `json:"claims"`
	Journal            map[string]*JournalEntry      `json:"journal"`
	Commits            map[string]CommitReceipt      `json:"commits"`
}

func NewSnapshot(binding string) *Snapshot {
	return &Snapshot{Version: "sessionowner-state/1", BindingID: binding, Credentials: map[string]any{}, Identity: map[string]any{}, Provenance: map[string]any{}, Fences: map[string]time.Time{}, Claims: map[string]*Claim{}, Journal: map[string]*JournalEntry{}, Commits: map[string]CommitReceipt{}}
}
func (s *Snapshot) Expected() Expected {
	return Expected{s.Generation, s.StateRevision, s.ClaimVersion}
}
func (s *Snapshot) Clone() (*Snapshot, error) {
	b, e := json.Marshal(s)
	if e != nil {
		return nil, e
	}
	var x Snapshot
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(&x); e != nil {
		return nil, e
	}
	return &x, nil
}
func (s *Snapshot) Validate() error {
	if s == nil || s.Version != "sessionowner-state/1" || !validID(s.BindingID) || s.Generation != "" && !validID(s.Generation) || s.Credentials == nil || s.Identity == nil || s.Provenance == nil || s.Fences == nil || s.Claims == nil || s.Journal == nil || s.Commits == nil {
		return fail(UnsafeStorage, "snapshot")
	}
	if len(s.Journal) > 4096 || len(s.Claims) > 1024 || len(s.Commits) > 16384 || len(s.CandidateTransfers) > 4096 {
		return fail(StorageFailure, "capacity")
	}
	return nil
}
func nextRevision(n uint64) (uint64, error) {
	if n == math.MaxUint64 {
		return 0, fail(StorageFailure, "counter_overflow")
	}
	return n + 1, nil
}
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("system random unavailable")
	}
	return hex.EncodeToString(b[:])
}

// MemoryBackend is a synthetic/reference transactional backend, not SQLite or
// proof of Linkcrawl's currently separate native claims and fences.
type MemoryBackend struct {
	gate             chan struct{}
	mu               sync.Mutex
	snapshot         *Snapshot
	FaultAfterCommit bool
}

func NewMemoryBackend(initial *Snapshot) (*MemoryBackend, error) {
	if e := initial.Validate(); e != nil {
		return nil, e
	}
	x, e := initial.Clone()
	if e != nil {
		return nil, e
	}
	b := &MemoryBackend{gate: make(chan struct{}, 1), snapshot: x}
	b.gate <- struct{}{}
	return b, nil
}
func (b *MemoryBackend) Acquire(ctx context.Context) (*Guard, error) {
	select {
	case <-ctx.Done():
		return nil, fail(OwnerBusy, "lock")
	case <-b.gate:
		return NewGuard(b, func() error { b.gate <- struct{}{}; return nil }), nil
	}
}
func (b *MemoryBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	if e := g.Check(b); e != nil {
		return nil, e
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshot.Clone()
}
func prepareCommit(current, next *Snapshot, expected Expected, id string) (*Snapshot, CommitReceipt, error) {
	if !validID(id) {
		return nil, CommitReceipt{}, fail(ProtocolMismatch, "commit_id")
	}
	if e := next.Validate(); e != nil {
		return nil, CommitReceipt{}, e
	}
	input, e := next.Clone()
	if e != nil {
		return nil, CommitReceipt{}, e
	}
	input.Commits = map[string]CommitReceipt{}
	raw, _ := json.Marshal(input)
	digest := Hash(raw)
	if old, ok := current.Commits[id]; ok {
		if old.Digest != digest {
			return nil, CommitReceipt{}, fail(StaleGeneration, "commit_reuse")
		}
		return nil, old, nil
	}
	if current.Generation != expected.Generation || current.StateRevision != expected.StateRevision || current.ClaimVersion != expected.ClaimVersion || next.BindingID != current.BindingID || next.StateRevision != current.StateRevision {
		return nil, CommitReceipt{}, fail(StaleGeneration, "cas")
	}
	rev, e := nextRevision(current.StateRevision)
	if e != nil {
		return nil, CommitReceipt{}, e
	}
	copy, e := next.Clone()
	if e != nil {
		return nil, CommitReceipt{}, e
	}
	copy.StateRevision = rev
	if len(current.Commits) >= 16384 {
		return nil, CommitReceipt{}, fail(StorageFailure, "commit_capacity")
	}
	// Never accept caller deletion of prior receipts.
	copy.Commits = map[string]CommitReceipt{}
	for k, v := range current.Commits {
		copy.Commits[k] = v
	}
	graph, proposal, err := settlementCommitWitness(current, next, id)
	if err != nil {
		return nil, CommitReceipt{}, err
	}
	r := CommitReceipt{OperationID: id, Certainty: "committed", Generation: copy.Generation, StateRevision: rev, Digest: digest, SettlementGraph: graph, SettlementProposal: proposal}
	copy.Commits[id] = r
	return copy, r, nil
}
func (b *MemoryBackend) Commit(ctx context.Context, g *Guard, e Expected, n *Snapshot, id string) (CommitReceipt, error) {
	if err := g.Check(b); err != nil {
		return CommitReceipt{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	copy, r, err := prepareCommit(b.snapshot, n, e, id)
	if err != nil {
		return r, err
	}
	if copy != nil {
		b.snapshot = copy
	}
	if b.FaultAfterCommit {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, fail(StorageFailure, "commit_ack")
	}
	return r, nil
}
func (b *MemoryBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	s, e := b.Load(ctx, g)
	if e != nil {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, e
	}
	if v, ok := s.Commits[id]; ok {
		return v, nil
	}
	return CommitReceipt{OperationID: id, Certainty: "not_committed"}, nil
}
