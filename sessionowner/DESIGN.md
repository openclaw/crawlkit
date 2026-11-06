# Candidate design and acceptance boundaries

## Implemented seams

- Strict UTF-8 JSON and framed transport, duplicate-key rejection, calendar and
  uint64 checks, closed protocol objects, and separately pinned provider schemas.
- Registry-owned wire and slot caps, including exact 87,423-byte synthetic Reddit
  cursor and 8MiB native body fixtures. These are distinct limits. Youcrawl's
  native cursor limit must remain its own 16KiB registry rule, never copied from
  its body cap.
- Authenticated channel admission, real confirmation/recovery callbacks,
  same-channel cancellation, durable request identity and uncertain outcomes.
- Transactional Guard/Load/Commit/Reconcile interfaces and protected Darwin file
  storage. A nonserializable Guard may wrap an existing native lock; owner keys
  are trusted comparable identities, normally backend pointers.
- Durable claim transitions and bounded evidence-based readiness resumption;
  private candidate proof before admission; separate state, credential and
  authority counters; preserved retry/security fences.
- Private lease-bound adapter frames, per-direction sequencing, durable
  checkpoint ACK before effects, parent-lifetime fencing and child-loss fixtures.
- Cookie selection uses Go's cookie jar with an explicitly supplied qualified
  public-suffix implementation, target/attribute policy and CSRF callback. The
  fixture's `.test` suffix implementation is synthetic, never a production list.
  Unsupported partitioning or unqualified SameSite semantics are rejected.

## Known remaining local work

The immutable C1 coverage receipt records historical gaps, not the current
implementation inventory. [B2A.md](B2A.md) supersedes its scoped uncertain-recovery
routing and permit-accounting gaps; [B2B1.md](B2B1.md) supersedes private staged
transfer refusal; [B2B2.md](B2B2.md) supplies bounded live-parent composition and
its native-total/outcome/cache corrections. The B2C0 inventory maps all 62 frozen
requirements to bounded exact-current, compatible inherited, static, local-gap
and external-blocked subclaims. These overlapping labels are not defect counts
or acceptance of whole requirements. Historical receipts are not rewritten.

1. Supported same-claim effect/commit reconciliation and nonrefundable original
   intent accounting have bounded closure. A generic gap label must not reopen
   C19/C38 or imply that ABI17 requires a native Node backend. Provider-authoritative
   outcomes, readiness proofs and native transaction qualification remain separate.
2. Private FD candidates require trusted expiry qualification and native custody.
   Cookie presence supplies neither expiry nor identity. [B2C1.md](B2C1.md) adds
   finite cookie selection/expiry/serialization/ownership, private-envelope and
   queued-rotation evidence. Its two demonstrated fixes now have parent-verified
   bounded independent closure; imported CSRF provenance and provider cursor semantics are not
   inferred from synthetic round trips or cursor byte limits. Its initial full
   race failure is retained. C1a separately measured a clean-child-exit/deadline
   confound and changed only the named FD3 fixture to explicit abrupt loss with
   stronger stage/reaping/history assertions. C1a production bytes and timers were
   unchanged; the old uncertain return was not reproduced or retroactively waived.
   Its changed-fixture exact-source gates now pass under bounded parent verification.
3. B2B2 live-parent composition is explicitly opted in, not an implicit Handle
   fallback. Pending restart cannot revive the original parent or borrow a fresh
   caller's deadline. The earlier B2B2a statement selecting non-resuming settlement
   for later work was its historical boundary; [B3.md](B3.md) now implements that
   separately authorized metadata-only candidate, without business continuation.
   Its named independent B3a closures are distinct from the subsequent
   [B2C2.md](B2C2.md) fallback/release/descriptor candidate. The coordinator-selected
   contract is now implemented with a finite 24-leaf owner table, but its changed
   error/lifetime/privacy/fence intersection still needs independent review.
4. [B2C3A.md](B2C3A.md) adds 30 shared test/corpus cases per language for selected
   kinds-only envelope caps, six private grammar kinds plus a staged variant and
   scalar/depth/node limits. Matching actual wire hashes distinguish byte parity
   from merely similar builders. This is not private Node runtime authority or
   exhaustive private state/lease coverage. Public queue saturation, cross-owner
   isolation, authority handoff, remaining private semantics and phase-reserve
   cases remain separately scoped; no arbitrary Cartesian expansion follows.
5. Registered result-schema checks and known-private-value refusal protect public
   results. Provider-specific redaction, cursor mapping, identity proof and
   transport/token semantics still need disjoint provider fixtures. No real SDK,
   account, browser or native-store proof follows from the common-core tests.
6. Complete relevant conformance, independent final-delta review and the supported
   pinned ClawSweeper route remain before acceptance. The current uncommitted
   candidate is not clean committed-range coverage; no auto-commit or stash is
   used to manufacture it. Distribution and per-consumer rollout remain separate.

## Native equivalence is a future qualification

Spotify's SQLite active-pointer transaction and Linkcrawl's private transaction
must implement the same logical snapshot and operation-ID readback. The default
file and in-memory tests do not qualify either native database. Preserve existing
native locks, identity checks, cursor logic, source receipts and qualified cookie
rotation. Linkcrawl's int Revision is not automatically a credential revision or
public authority epoch; public/source receipt replay is not session commit
reconciliation.

## Explicit exclusions

No root-module import, root dependency/default change, root workflow edit,
consumer dependency update, installed artifact change, live enrollment, provider
request, SDK import, browser start, service, scheduler or publication. Bird/Birdcrawl
and consumed dependencies remain frozen; X, Main, remote readers and SQL holds
remain unchanged. No Peter environment was inspected.

## Separately admitted metadata settlement candidate

[B3.md](B3.md) records the explicit default-deny non-resuming terminal route,
prospective admission anchors, stable preparation/resolution identities and the
bounded positive/blocked matrix. It is an offline partial qualification, not a
host-policy enrollment, full conformance or consumer-ready release. Original
business deadlines, effect counters and independent claim/security fences remain.

B3a binds the complete original terminal dependency graph and proposal to native
backend receipts, rather than trusting a mutable facts list. Per-load proof and
control-boundary coverage and completed bounded independent closure are detailed
in B3.md. Finite residual local contracts and independent review of subsequent
source deltas remain; neither full B3 acceptance nor a consumer gate is implied.


## B2C3B bounded ordinary-admission qualification

[B2C3B.md](B2C3B.md) records thirty new test-only auth/isolation/ordinary-queue and
phase-reserve cases, including actual sixteen-unfinished saturation before the
seventeenth request, exact cleanup accounting and unconditional fixture cleanup.
Production, existing tests and all Node/A-corpus inputs remain frozen. Full
conformance and independent semantics review are separate from these owner gates.

The parent-verified bounded C2 closure is now recorded below and in B3/B2C2. It
arrived after A's freeze, so this update does not rewrite A's saved evidence.


## Subsequent parent-verified bounded C2 closure

The combined closure `e31608cddb7f19f1c61e46c5ede671eafe8d0c2b173555affe9e2678875ae59c`
now verifies the selected finalizer/error/privacy/fallback scope on its immutable
100-file source. One independent gate passed six leaves/seven records in 23.729s,
with five actual inner releases, one failed Acquire, zero writes/business effects
and empty error statuses. The owner twenty-four-case proof was separately inspected,
not replayed. Earlier statements requiring this targeted review record the preceding
candidate boundary; they no longer describe it as pending. Full B3/all-62/native,
provider, OS, distribution and consumer acceptance remain separate. The original
late partial advice stays late and unverified, not retrospectively accepted.


## C5 finite local assertions

[B2C5.md](B2C5.md) adds exactly four test-only leaves for actual cold readiness
transient/same-claim resume, protected temporary reopen with uncertainty refusal,
and two new owned-directory mode guards. Positive no-effect checkpoints and exact
native receipts distinguish readiness evidence from an error alone. Internal
controls and the initial closed-schema fixture correction are recorded separately.
All 95 earlier non-Markdown inputs remain frozen; no production, environment,
provider or runtime change follows. This local owner evidence is not full-conformance
or consumer acceptance. Parent documentation consolidation remains separate.
