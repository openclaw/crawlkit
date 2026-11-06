# B2C3B: ordinary admission, isolation, queues and phase reserves

This test/docs-only slice adds exactly B01-B30 in three new Go test files. All
production code, existing tests, Node/A-corpus inputs, ABI, FD3 fixtures and
package/dependency manifests remain unchanged. No parser, authority or runtime
fix is introduced. The private case map records exact source and executed stages.

## Finite cases

- `TestB2C3BAdmissionIsolation`: B01-B10 and B28 cover authentication refusal,
  unenrolled admission, operation/target Allow denial, confirmation/recovery denial,
  foreign-owner channels, wrong bindings, separate caller journals, independent
  owner-purpose state and a forbidden public maxRpcMs selector. Valid registered
  shapes precede injected denials; storage, callbacks, counters and history are
  checked. Same-binding callers intentionally share that authority's credential
  context but never another caller's cached public result. Distinct bindings use
  separate backends and private sentinels, not two writers on one store.
- `TestB2C3BOrdinaryQueue`: B11-B14 and B29-B30 use real MemoryBackend gates and
  guards, channel handshakes and bounded cleanup. B11 observes one held guard,
  fifteen failed real-gate acquisitions, fifteen live waiting contexts and sixteen
  actual unfinished Owner entries before request seventeen. The active map is only
  inspected, never fabricated. The seventeenth must fail before Acquire. All
  canceled waiters settle before the holder is released. Closed channels cannot
  revive; a fresh channel can admit a previously undispatched request. Foreign
  caller/binding cancellation leaves the original context, receipt and permit intact.
- `TestB2C3BPhaseReserve`: B15-B23 independently calculate nine reserve boundaries,
  with exact-positive and one-millisecond-short checks. Unique remaining phases
  contribute their costs; positive action reserves one millisecond, and cleanup
  is added only when absent. B22 supplies `[cleanup]` once, never a duplicate.
  B17 also checks qualified warm startup omission. B24-B27 exercise only the four
  actual runPhase callback mappings: startup, capture, proof and action. Baseline
  callbacks observe the clipped deadline; an outer window equal to the remaining
  commit/cleanup/IPC tail refuses before callback, storage or provider work.
  Import, commit, cleanup and IPC are reserve-model cases, not invented callbacks.

## Clock and lifecycle boundaries

Non-temporal operation/history fixtures use the existing owner-local controlled
clock. This does not control the ordinary queue: Owner.Handle still constructs
its real `context.WithTimeout` with the unchanged one-second queue allowance.
B14 observes that actual queue context reaching DeadlineExceeded while the native
held guard remains valid. The existing backend maps canceled, closed-channel and
expired waits to OwnerBusy; these tests do not invent a different cancellation code.
No sleep is used as stage proof, and no timeout or production budget is raised.

The instrumented backend uses the same MemoryBackend gate. Its failed nonblocking
acquisition records actual contention before the original bounded blocking select;
there is no second semaphore or authority. Every launched operation has cancellation,
buffered result delivery, an unconditional release path and a bounded goroutine join.
The successful leaves include cleanup completion, not merely requested cancellation.
At most seventeen ordinary requests are created in a fixture; cases run sequentially.

The phase tests are controlled direct-method deadline fixtures with registered
request shapes and synthetic owner-selected plans. They do not run recovery or
provider callbacks and do not qualify actual SDK/browser startup or native budgets.
No-entry plus unchanged backend/state proves that the negative callback could not
invoke BeforeEffect. They are not a substitute for real-provider phase qualification.

## Evidence and review

The first new-only gate passed all thirty leaves (thirty-three records including
three group totals). No production or fixture defect was reached. Final-source
namespace core/race/vet/format/docs outcomes, the exact case map, immutable source,
resource/process custody and any failures are recorded under `c1b2c3b` in the parent
evidence root. Final core includes these cases, with no redundant focused replay.
Node is not rerun because all of its inputs, including A's corpus and driver, are
byte-identical. [B2C3A.md](B2C3A.md) remains a separately reviewed wire-test scope.

The prewrite plan was saved before test writes but missed its 180-second target by
15.876133 seconds. Original source/gate/report cutoffs were retained without reset.
Passing this finite table does not establish all 62 requirements or native/provider,
SDK, OS, distribution or consumer acceptance; independent semantics review remains.

## C2 review status reconciled in this later owning pass

The parent-verified combined C2 closure `e31608cddb7f19f1c61e46c5ede671eafe8d0c2b173555affe9e2678875ae59c`
accepts only its bounded finalizer/error/privacy/fallback intersection. Its independent
six leaves/seven records passed in one gate, with zero writes/business effects and
empty error statuses. It arrived after A's source freeze, so A's immutable artifacts
were not changed. This admitted B documentation pass reconciles the current status
without rehabilitating the old late partial advice or asserting full B3 acceptance.


## Later bounded B review disposition

The parent accepted the bounded independent static B semantics review, bound by
combined closure `62d302bacc4c13c074ea88fcb16f25e57b3f3d52282b561b4bed71ad71170b98`.
Its input and proof bindings were verified separately; no owner test replay or
whole-row/full-conformance acceptance follows. The original 15.876133s plan-target
lateness remains retained.
