# Session owner

`github.com/openclaw/crawlkit/sessionowner` is a provider-neutral package in the
existing `github.com/openclaw/crawlkit` Go module. It uses Crawlkit's normal module
version and release, without a nested module, separate tag or packaging workflow.
The private thin Node binding remains source within Crawlkit, not a separately
published artifact. No provider, browser, Gateway, credential discovery, SDK
import, timer or background service is included; adding the package does not
register it with Crawlctl or activate any consumer.

See [distribution with Crawlkit](RELEASE.md) for validation and adoption boundaries.
The stage documents below retain historical offline qualification, including the
superseded nested-module setup; they do not override the current package layout.

The source implements the frozen `sessionowner/1` public protocol and
`sessionowner-adapter/1` private protocol. The five schemas **and** normative
manifest in `schema/v1/` are the contract. Protocol types are closed; provider
payload slots require a second validation against immutable registry-pinned
schemas. Unsupported schema keywords fail closed. This is a deliberately bounded
schema dialect, not a claim of full Draft 2020-12 implementation.

## Host integration

1. Construct `NewRegistry(policyBytes, expectedSHA256, schemaArtifacts)`. Owner
   code pins the registry and schemas; callers cannot select schemas, transport,
   URLs, headers, browser code, profiles, credential paths, limits or plans.
2. Supply a `Backend`, trusted `Authorizer`, and artifact-bound `Adapter` objects
   to `NewOwner`. `AuthenticateChannel` receives the real authenticated transport
   peer. A JSON caller ID is never authentication.
3. Bound framing with `ReadFrame` and `Registry.ChannelCap`, then call
   `Owner.Handle`. Public cancellation requires the same caller, live channel and
   binding as the original request. An acknowledgment is not completion.
4. An adapter receives a private `Invocation`. `Step()` is owner-selected.
   `BeforeEffect` persists intent before allowing an effect. `RequestContext`
   retains the native per-request ceiling inside the remaining phase deadline.
   `AcceptProviderBody` and `AcceptCursor` enforce separate native slot limits.
5. Node business callers use `bindings/node/OperationClient` over an injected
   authenticated transport. It opens no credentials or services. A trusted Node
   adapter can use the separate inherited duplex descriptor with `AdapterSession`
   and `RunPrivateFD`; it cannot publish active state or recursively acquire the
   authority lock. The trusted same-user boundary is not hostile-user isolation.

`Inspect` is local metadata only. Initial enrollment is not automatic. Recovery
needs an existing enrolled identity and a real authorization callback. Exact
identity proof and response-delta callbacks are mandatory where used; missing
provider qualification fails closed. Mutations are never blindly retried.
`SafeReadRetry` is a separate bounded owner-composition helper, not an automatic
login mechanism or a wrapper around mutation batches.

## State and budgets

Authority generation is an opaque admission epoch. Routine coherent cookie/CSRF
changes advance internal credential/state revisions without changing that epoch.
Existing cursor compatibility remains provider-specific.

The default protected-file backend currently qualifies only local Darwin APFS/HFS.
It checks ownership, regular files, permissions, links, ancestors and lock inode;
uses one stable advisory lock; and atomically publishes the complete snapshot and
commit receipt. Local reconciliation synchronizes a visible commit before claiming
it committed. Other platforms fail closed. `MemoryBackend` is synthetic/reference
storage, not SQLite durability evidence.

`Backend` also permits native transactional implementations. Spotify must stage an
immutable protected payload then commit its active reference and all authority
metadata in one native SQLite transaction. Linkcrawl's existing single-record CAS
is useful, but its common claims, journal and fences are **not yet** one qualified
transaction. Its paired source publication is not cross-database session atomicity.
Do not install a second manifest, lock or cookie manager around either native owner.

Budget plans are owner-selected ceilings and remaining-phase reserves. An action
ceiling is not a mandatory reservation of the entire native run window. Completed
queue/capture phases are not reserved again. Every phase remains clamped to the
original outer deadline, with cleanup reserved. Warm-only plans cannot become cold
startup, and a separate recovery cannot revive an expired business operation.

## Validation

Run package validation in the root module with its required Go 1.27.3 toolchain.
Removing the nested `go.mod` puts this directory under the existing root
`go test ./...` and `make check` entrypoints; no extra workspace, replacement or
CI job is needed. Compilation and tests in this root-module context remain a
required validation gate before landing, not a result inferred from file layout.

Earlier offline stages used a separately admitted cached Go 1.27.1 route. Their
receipts remain historical behavior evidence, not permission to bypass the root
toolchain or replay older module/vendor workarounds. This package-layout change
does not rewrite the implementation, existing tests or Node/schema contracts.

The shared corpus has 38 valid/invalid cases. Executable fixtures cover large
cursor/body limits and escaping, guarded CAS/readback, cross-process contention,
file crash boundaries, native/Node wire validation, FD3 child loss before/after
ACK, identity-proof callbacks, rotation, cancellation and no mutation replay.
Exact gate receipts and honest C01-C42/ABI01-20 status live in the parent evidence
root, not in this repository. Passing the current suite does not claim exhaustive
conformance or provider acceptance.

See [DESIGN.md](DESIGN.md) for remaining acceptance work and
[RELEASE.md](RELEASE.md) for normal Crawlkit distribution and consumer gates.

## Focused hardening

[B1.md](B1.md) records the six implemented review fixes, shared regressions and
remaining interoperability limits. It does not supersede the open work in DESIGN.md.

[B2A.md](B2A.md) describes exact same-claim recovery, effect/commit reconciliation
and permit-based attempt accounting. [B2B1.md](B2B1.md) covers the protected
private candidate-transfer path; composed safe-read retry and broader conformance
remain separate.

B2A.md also records the closed persisted-phase validation and subsequent IR1/IR2
intent/candidate corrections. Exact-source race closure remains an explicit gate.

The A3 section of B2A.md separates non-temporal recovery fixture clocks from
explicit deadline/cleanup-expiry tests; production defaults and limits are unchanged.

The A4 section of B2A.md defines durable intent preparation before counter/witness
publication. Restart reconciliation no longer relies on best-effort later markers.

B2B1 retains the exact decoded candidate, transport bytes/digest and qualified
expiry before its acceptance transaction. Private inline input requires an
explicit expiry qualifier, while protected staged input uses fixed trusted
repository scope. Large proof candidates use a durable opposite-direction grant
and `AdapterSession.ConsumeStagedCandidate`, not oversized silent inlining. No
public ABI bytes, native adapters, provider transport or installed consumers change.

The B2B1a correction enforces exact protected metadata keys, clamps candidate
handoff reads/commits to the original lease, candidate and parent bounds, and
cancels parent loss independently of the session mutex. Delivery and retirement
now retain separate original transaction IDs before attempting their transitions;
unknown outcomes block new handoffs until exact readback. See B2B1.md for executed
boundaries and the remaining ACL, stable-root and native-bridge qualification.

[B2B2.md](B2B2.md) describes the explicit `Owner.HandleWithRecovery` live-parent
composition candidate. Registered/authenticated safe reads can recover under the
same guard and retry once within their original deadline and durable shared
reservations. **B2B2 is partial:** pending interrupted compositions are readback-only
and cannot resume effects from a fresh parent/context; the current frozen request
and authorizer do not attest that the original parent remains live. Completed
final outcomes can be read back exactly. No implicit fallback is added to Handle.

B2B2a corrects child native-total admission before approval/storage and qualifies
multi-request permit/body accounting, contradictory outcome witnesses and final
delivery cancellation precedence. See B2B2.md for exact behavior and limits.
Pending lost-parent composition remains fenced; terminal settlement is not part
of this correction or a consumer-readiness claim.

B2B2b adds the missing snapshot-versus-committed-receipt revision lower bound and
preserves the original known composed-cache certainty/receipt for authenticated
pre-entry canceled or expired delivery. Equal/current and strictly later history
still work. Authorization precedes the memory-only refusal; no backend replay,
new deadline, terminal settlement or fence clearing is introduced.

[B3.md](B3.md) adds the separately admitted offline private non-resuming metadata
settlement candidate. Its capability is default-deny, uses the existing guard,
retains original transaction identities/counters/history, and never resumes or
schedules business work. Prospective registry/artifact anchors are mandatory;
legacy missing anchors and unresolved effects remain explicitly blocked. This
partial qualification does not enroll real hosts or authorize deployment. The
completed named independent B3a closures and remaining finite local contracts
are distinguished in B3.md.

B3a corrects the historical dependency-omission fence defect: original dependency
roles/links are derived at preparation and bound by native backend receipt
witnesses, then all required IDs are re-read before any exemption. The bounded
matrix also covers claim/route precedence, protected guard contention, concurrent
controls and control cancellation delivery. See B3.md and the exact coverage
receipt; runtime enrollment and broader native qualification remain ungranted.

[B2C1.md](B2C1.md) adds 39 finite cookie, private-envelope and rotation/cursor
cases. Reached failures led to owned cookie Records/Unparsed copies and a private
result check against the unchanged leased credential base; journal-only state
revision advances remain valid. Cookie serialization preserves absolute expiry,
not a new relative lifetime, and legitimate multi-origin reload is retained
without inventing missing CSRF provenance. A deterministically queued follower
observes the newer credentials under the unchanged authority epoch. These are
bounded common-core assertions, not real provider cursor, browser or native-store
qualification. The parent has verified bounded independent review of both C1
production fixes. Their reviewed production bytes remained unchanged through C1a;
all Node/schema/corpus inputs remain frozen.

The C1a section of B2C1.md records the retained failed race gate and a separately
measured FD3 fixture confound: race-instrumented clean child shutdown consumed the
one-second action window instead of representing abrupt pre-ACK loss. The named
fixture now uses an explicit nonzero pre-ACK exit and checks exact exit stage,
reaping, permits and original history without changing real clocks, production
limits or certainty assertions. The old uncertain return did not reproduce during
the single diagnostic and is not retroactively explained or waived; the changed
fixture has its own passing final gate receipts. C1a production bytes were unchanged.

[B2C2.md](B2C2.md) defines the subsequent coordinator-selected private settlement
finalization contract. The owned guard is released once before delivery: a safe
prior body error wins, otherwise current lifetime governs every status (including
blocked), then a release error is surfaced. Errors return empty status and fixed
safe diagnostics without changing known commits, original IDs, history or counters.
A finite 24-leaf fallback/release/descriptor table is owner-qualified; independent
current-source error, lifetime, privacy and fence review remains required. No
backend/commit/graph, business, cookie/adapter, FD3, Node or schema rewrite follows.

[B2C3A.md](B2C3A.md) adds a compact shared Go/Node recipe corpus and exactly 30
new cases per language for kind-specific request/response caps, private grammar
and scalar/depth/node limits. Actual wire hashes, byte counts, outcomes and
expectations are compared, with controls and old Node regressions counted
separately. The private tests use the existing generic schema validators, not a
Node authority, SDK or writer. All production code, frozen ABI and old corpus
inputs remain unchanged; C2 review and wider conformance/consumer gates stay
separate.


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
