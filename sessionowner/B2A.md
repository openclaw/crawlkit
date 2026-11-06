# C1B2A: scoped recovery resumption

This offline slice closes recovering-only ordinary-effect admission and implements
the existing recovery hooks. It does not authorize integration or consumers.

## Admission and durable identity

Both uncertain and recovering state fence ordinary effects. Qualified registered
safe reads retain B1's explicit exception; Recover has no blanket bypass. Resume
must match stored binding, epoch, claim, authenticated caller, original recovery
journal and current authorization. Other active claims or unrelated uncertain
journals block it. Inconsistent candidate/proof hashes, effect IDs, phase/counter
or claim/journal relationships fail closed. Old unbound claim records are not
silently adopted. An accepted claim can return its retained committed outcome
through exact authorized readback, including its original epoch and receipt.

A non-replacement new recovery needs a stored typed diagnosis from a qualified
safe read: actual failed response, request ID, caller, accepted epoch, registered
operation/adapter and exact rejected_generation/expired_session cause. AuthBlocked
or a journal row alone is insufficient. Explicit authorized replacement remains a
separate owner-authorized path, never implicit enrollment or account selection.

## Recovery effects and cancellation

Preparing ACQUIRING/PROVING is not a consumed external attempt. The first durable
before-effect permit consumes the attempt, records effect IDs and claim/journal
correlation before permission. Further requests in the same step retain that
attempt and append effect witnesses. No consumed attempt is reset or refunded.
Before-permit interruption retains a finite same-claim continuation; a retained
candidate resumes proof without capture. After possible effects, the phase remains
EFFECT_UNCERTAIN and cannot reacquire or replay a business mutation.

The pinned adapter may implement RecoveryReconciler. Its input binds caller,
binding, epoch, claim, original request, step, effect IDs and candidate digest.
The returned evidence must match exactly. Reconciliation has the existing phase,
RPC, request and outer limits. It may recover the original candidate/proof, prove
rejection/no-effect, or remain uncertain. No-effect after a consumed acquisition
or proof does not refund its budget. Readiness no-effect can resume under the
existing finite allowance and cooldown. Candidate expiry/rejection stops reuse.

## Publication and exact readback

COMMITTING persists a specific commit ID, proposed generation and expected base
revision before publication. Credential/identity/proof/claim/journal/outcome still
commit atomically through the existing Backend. Uncertain publication never
publishes the mutated desired snapshot as if it were known active.

A fresh authorized resume queries that exact backend commit. Positive matching
readback returns the retained outcome without acquisition or another commit.
Proved not-committed may publish the same still-valid candidate/proof under a NEW
commit identity; the uncertain ID is never blindly repeated. Publication attempts
are capped at three per claim and prior commit IDs remain recorded. This is a
conservative local safety bound, not permission for automatic retries. Unknown or
contradictory readback stays fenced. All provider/security/recovery deadlines and
consumed counts survive; observational local readback does not clear them.

## Validation and remaining work

Fresh synthetic tests cover recovering-only exclusion, exact caller/claim resume,
real versus fabricated rejection references/causes, lost-acquisition reconciliation
without recapture, both committed and not-committed publication readback, pre-permit
cancellation/counts, expiry stops, and private-file retained-candidate restart.
Earlier B1 regression tests remain in the full suite. Current gate/coverage/custody
receipts are private under c1b2a, preserving the prior red review baseline.

B2B still owns the protected staged-candidate resolver, digest/expiry/proof mapping
for that boundary, and fully composed safe-read recover/retry. Wider interruption,
private/cookie/cursor/handoff/native-backend matrices and independent review remain.
These are local work items, not excuses based on unavailable SDK/provider access.
Native transactions, live identity/SDK/loaded-artifact proof, distribution and all
runtime/held-fleet gates remain separate. Frozen ABI schemas were not changed.

## Subsequent L1 and IR1/IR2 corrections

The persisted state reader now enumerates all twelve supported claim phases.
Unknown, empty or malformed phases cannot become inactive/terminal or escape
admission. Current reachable counter, candidate/proof, permit-journal and commit
relationships are validated at the loaded-state boundary. Invalid state returns
only safe failure/uncertain status; no raw phase is reflected and no normalization,
deletion, refund or recapture repairs it automatically. Valid intermediate and
terminal state fixtures and private-file reload are included in the correction.
This is not exhaustive model checking or native-backend qualification.

Before-effect counters and witnesses are now staged in a private snapshot. Only
positive matching committed receipt/readback installs that state. If the exact
intent transaction is positively proved not committed and a guarded readback
matches the authoritative pre-intent snapshot, cleanup resumes from that state,
not the uncommitted proposal. A no-effect continuation retains its existing
cooldown and finite allowance. An issued or ambiguous intent is never refunded;
unresolved intent cleanup does not publish either a proposed witness or an
assumed no-effect state. Failure evidence remains in the immutable gate receipts.

One private candidate-admission function is shared by ordinary acquisition and
reconciled candidate_ready. It checks bound scope, schema/byte custody, expiry,
retained candidate/proof binding, currently rejected credentials and historical
rejected digests. A reconciliation receipt proves provenance, not an exception to
those fences. Refusal does not clear auth stops or trigger proof/capture replay.
A valid new candidate still requires the existing exact-identity proof path.

The prior race failure (native=true, rpc=false, phase=false) is retained. The
independent bounded diagnosis demonstrated a scheduling-sensitive assumption:
construction/validation or durable admission could exhaust the declared5ms lease
before the original three flags were reached. It did not prove the exact historical
branch or erase the failure. The corrected fixture controls only owner-local test
deadline scheduling: production still uses context.WithDeadline, the5ms limit and
pre-admission start point are unchanged, and no global/runtime setting is exposed.
Tests separately require native work to retain its own window, private frame/lease
bounds for both nesting orders, admitted RPC cancellation at its derived deadline,
and zero callback/effect/ACK on pre-dispatch expiry. Missing clamping, overstated
phase bounds or global native clamping still violate explicit assertions. Final
exact-source race results, not the diagnostic's focused passes, decide the gate.

A4 supersedes the best-effort post-failure marker as the safety boundary. Before
attempting any intent counter/witness transaction, the owner positively commits a
preparation record in the same existing guarded journal. It binds the planned
transaction and effect IDs to exact binding, caller, request/digest, operation,
claim, epoch, step/phase and pre-intent counters. If preparation is not positively
durable, the actual intent transaction is never attempted.

The counter/witness transaction uses that pre-recorded ID and atomically marks the
record committed. If it remains unknown, restart finds the preparation even when
all later cleanup/marker writes fail. A fresh scoped resume must query the exact
original ID before another related permission. Definite not-committed resolution
retains zero never-consumed quota and permits a bounded continuation. Committed
witnesses stay consumed and enter the existing effect reconciler, never acquisition
replay. Unknown resolution remains fenced. A genuinely never-attempted reservation
is still resumable; absence or an error is not used as proof. Legacy marker readback
is retained, but new safety does not depend on publishing a marker after failure.

Fault tests classify preparation versus actual intent by semantic state/transaction
identity and assert the intended stage was hit. They cover nonpositive preparation,
the original two-storage-failure/restart scenario, unknown and definitive original
readback, failed cleanup and actual private-file child crashes before/after intent.
A ported3ms successful-admission regression retains the original5ms RPC deadline;
no budget/default/start-point is enlarged. Full-matrix/native/backend qualification
and independent final-diff closure remain separate.

## A3: distinguish rejection invariants from cleanup timing

The single instrumented IR2 race diagnostic observed failed/storage_failure/
uncertain responses without a claim after one authorized capture. The original
and capture windows still had about20s/500ms available; the separate2ms commit
plus2ms cleanup window expired while committing or reading back EFFECT_UNCERTAIN.
A controlled expiry of that unchanged cleanup budget produced the same safe
missing-claim shape. One valid-new observation retained its claim. This establishes
the mechanism and fixture assumption, not every unlogged historical branch or a
harmlessness verdict. The original red gate and diagnostic source/log remain.

Non-temporal recovery-state fixtures now use one shared fixtureRecoveryOwner
clock/deadline policy. It freezes only the owning test instance's timeline while
checking claims, counters and rejection fences. Production still defaults to the
same context.WithDeadline timers and declared limits. Real deadline tests retain
the ordinary fixture, and explicit cleanup expiry is a separate controlled test
that requires uncertainty, no unconfirmed claim and no refunded issued permit.
No timeout was widened and no missing claim is skipped.

IR2 tests must first establish a failed/uncertain EFFECT_UNCERTAIN claim, one
capture and no proof. Only then do they test current/prior rejected fingerprints
or valid-new success. Their assertions still reject the retained prior bug's
recovered/proof-called outcome. The original diagnostic was moved to immutable
private evidence after its one execution, not repeatedly included as a product
gate. A compile-only arity error before that execution was retained separately.
Final exact-source gates decide acceptance; focused or instrumented passes do not.

The A4 full race gate also exposed a non-temporal B1 journal/fence assertion using
the same tiny real-time cleanup fixture. Its focused observation showed the
qualified read did execute (effects=2) but result publication returned
storage_failure/uncertain. The existing controlled state-fixture helper now covers
non-temporal journal, fence, rotation and disk-restart assertions coherently, while
real deadline/admission-expiry tests keep their temporal fixtures. No rejection,
receipt, counter or deadline assertion was removed. Both failed race and focused
observation are retained; final current-source race remains the required gate.
