# Test cleanup ledger

Scope: crawlkit, photoscrawl, imsgcrawl, crawl-remote, graincrawl, notcrawl,
discrawl, and gitcrawl. Land narrow PRs; no releases.

Remove tests only with evidence of redundancy, no meaningful assertion, or an
obsolete contract. Keep subprocess helpers and isolated integration fixtures
when their callers verify behavior. Unit tests are not end-to-end proof.

## crawlkit: release notification decisions

- Replaced `TestStderrIsTerminalReturnsBool`: it discarded the result of a thin
  dependency wrapper. Its replacement redirects stderr to a pipe and asserts
  non-terminal detection. Removing the only local caller exposed the exported
  helper to CI dead-code detection; keep a meaningful test of its contract.
- Extended the existing `ShouldNotify` decision table with interactive success,
  explicit `check-update` suppression, and bare `--json` suppression. The
  positive case detects an implementation that simply always returns false.
- Retained notification writer/error tests and subprocess helpers: they check
  distinct output, error handling, or child-process behavior.
- Validation: workspace-disabled module tidy (unchanged module files), vet,
  and uncached full Go suite; structured autoreview before landing. PR CI runs
  Linux and Windows suites plus the pinned downstream gitcrawl comparison.

## crawlkit: vector assertions

- Removed `TestFloat32EncodingRoundTrip`: `TestFloat32EncodingMatchesBinary`
  already verifies encoded bytes and decoded float bits, including arbitrary
  finite/non-finite bit patterns, nil/empty vectors, and invalid byte lengths.
- Removed the single-use `InDelta`, `Greater`, and reflection-based `Nil`
  assertion helpers and their private nil detector. The first two silently
  accepted NaN because both `NaN > delta` and `NaN <= other` are false.
- Reused the existing finite-aware kernel number checker for cosine results;
  check rank ordering and the nil TopK result directly. Kept both rank-fusion
  cases: accumulated rankings and tied scores are distinct contracts.

Validation: full local gates, focused vector race tests, synthetic NaN failure
probes, and PR CI (including SIMD/scalar variants and pinned downstream CLI
comparison). No production APIs or behavior changed.

## Remaining audit

- crawlkit: review remaining packages, fixture consumers, duplicated coverage,
  flaky timing assumptions, and behavior gaps.
- photoscrawl, imsgcrawl, crawl-remote, graincrawl, notcrawl, discrawl, gitcrawl:
  full test and fixture audit pending; validate each change against its actual
  app behavior using isolated archives and platform-appropriate execution.

## crawlkit: HTTP fixture assertion failures

- Replaced fatal assertions in remote HTTP handlers with non-fatal test errors,
  an HTTP error response, and a normal handler return. `FailNow` belongs to the
  test goroutine; in these callbacks it aborted the connection and exposed an
  unrelated EOF to the client.
- Keep every request assertion and expected response. Root-test fatal assertions
  remain intact, and failed handlers stop before sending the success response.
- All fixtures stay local and synthetic; no production transport or API changes.

## crawlkit: remaining provider and update-check handler failures

- Replaced the two remaining direct fatal assertions in embedding-provider and
  release-check HTTP callbacks with nonfatal errors, HTTP 400, and return.
- Preserve request conditions, successful embedding and cached-update responses,
  vector/auth/dimension assertions, cache counts, and root-test fatal assertions.
- Forced decoder/path failures reproduce EOF before the fix and fail with the
  original diagnostic plus HTTP 400 afterward. Full local gates, independent
  review, and PR CI validate synthetic fixtures; no live APIs or releases.

## crawlkit: strict manifest fixture decoder errors

- Make the strict SQLite bundle fixture decoder return an error. The root test
  still fails fast; the HTTP callback reports the error, sends HTTP 400, and
  returns normally. Keep unknown-field and trailing-JSON rejection unchanged.
- Preserve success payloads, addressing-mode checks, manifest fields, upload
  counts, and the file-handle closure check for both legacy and snapshot modes.
- Forced unknown fields and trailing JSON reproduce connection EOF before the
  change. Afterward, root failures retain their diagnostics and uploads report
  typed HTTP 400 errors without EOF. Validate with race-enabled probes, full
  local gates, independent review, and PR CI; no production/API changes, live
  archives, credentials, or releases.

## crawlkit: worker fixture phase waits

- Bound four handler-start waits using the same ten-second budget as the
  existing eventual assertions. A missing handler now fails at the waiting
  test with a useful diagnostic instead of hanging until the suite timeout.
- Let the paused input-edit handler observe cancellation so test cleanup can
  stop its runner after a setup failure. Keep all queue, freshness, revision,
  cancellation, and independent-kind assertions and synthetic SQLite fixtures.
- Temporary Go overlays reproduce the global timeout and stuck cleanup before
  the change, then verify the targeted failure and clean shutdown afterward.
  Validate with the full local gate, independent review, and PR CI; no
  production code, exported APIs, live archives, or releases change.

## crawlkit: vector helper process failure status

- Move the helper's success exit from a defer to its normal completion path.
  Panics must fail the helper process, including after it writes valid JSON;
  deferred success previously concealed those failures from the parent test.
- Preserve every bridge request check, canned response, mode, and test body.
  Healthy completion still exits before the test runner can append output.
- A temporary post-response panic leaves the existing bridge test green before
  the fix and fails it afterward. Validate healthy bridge cases with the race
  detector, full local gates, independent review, and CI. No production APIs,
  live archives, provider calls, or releases change.

## crawlkit: ordering assertions require rendered content

- Require the selected-message heading and both sorted document fields before
  checking their order. Use the complete heading when locating the selected
  message; a missing string previously returned -1 and passed the comparison.
- Preserve both rendering fixtures and all existing content/order assertions.
  Disposable post-render overlays show that a missing heading, first field or
  both fields passes before the fix and fails afterward; reversed fields fail
  in both versions. Healthy race-enabled rendering tests, full local gates,
  independent review and CI validate the change. No production or API changes,
  live archives, provider calls, or releases.

## crawlkit: compact document preview retention

- Require all 18 lines of the existing compact document preview, rather than
  accepting any count below the ceiling. Preserve the 30-line fixture and the
  expansion-hint assertion; no production code or rendering behavior changes.
- Disposable post-render overlays show that missing all body lines or one body
  line passes before the fix and fails afterward. Excess lines fail both
  versions. Validate healthy rendering with the race detector, the full local
  gate, independent review and PR CI. No live archives or releases.

## crawlkit: cancellation claim read errors

- Require a successful token read before accepting cancellation claim release.
  A failed read must not leave an empty token that falsely passes the assertion;
  the pending-job count alone does not prove the claim token was cleared.
- Preserve the real runner, cancellation, duplicate-run guard, pending count,
  temporary SQLite queue and timing. Disposable Go overlays compare healthy
  runs, broken reads, retained claims and both faults together before and after.
- Validate with the race detector, full local gates, independent review and PR
  CI. No production APIs, live archives, provider calls or releases change.

## crawlkit: stale cleanup preserves kept ciphertext

- Extend the existing public backup-helper case to read the kept shard before
  cleanup and require identical bytes afterward. Preserve its stale-file removal
  assertion and all encryption, manifest and helper checks; add no fixture.
- The full original backup suite falsely passes when cleanup deletes or corrupts
  kept row-shard ciphertext. Both faults fail the strengthened case. Healthy
  suites pass, and skipped cleanup fails the stale-file assertion in both versions:
  eight race-enabled before/after outcomes. Production files are unchanged.
- Validate the full library gate, independent P0–P3 review and PR CI, including
  Windows and pinned downstream CLI comparison. No public API, live archives,
  provider calls or releases change.

## crawlkit: duplicate cache path rejection

- Remove the standalone escaped-name test. The existing non-basename table
  already covers the same path, requires the validation diagnostic, preserves
  an existing outside file and rejects cache debris. Keep that case unchanged.
- Nine race-enabled before/after outcomes confirm both cases reject a bypassed
  name guard. Deleting the outside file while returning the validation error
  falsely passes the removed case but fails the exact shared row in the retained
  case, before and after deletion. Healthy variants pass; production is unchanged.
- Validate the full library gate, uncached Go suite, independent P0–P3 review
  and PR CI with Windows and pinned downstream CLI comparison. No public API,
  fixture, live archive, provider call or release changes.

## crawlkit: SQLite snapshots preserve literal paths

- Preserve literal source and destination paths instead of trimming whitespace.
  The original suite missed selection of a neighboring database and replacement
  of a neighboring archive. Reject trailing spaces and dots in Windows path
  components before filesystem access, following the existing snapshot policy.
- New temporary SQLite fixtures contain distinct rows for neighboring sources;
  destination checks require both the intended database contents and unchanged
  neighboring bytes. Reintroducing either trim independently fails its regression
  case; healthy race-enabled tests pass. Native Windows cases require ambiguous
  path rejection while preserving both canonical files.
- Validate the full library gate, uncached Go suite, independent P0–P3 review,
  PR CI and supported downstream compatibility checks. No live archives, provider
  calls or releases. The separate store path-normalization boundary remains a
  follow-up.

## crawlkit: SQLite stores preserve literal paths

- Preserve literal filesystem paths in both writable and read-only store opens.
  The original tests missed reading a neighboring database and applying schema
  writes to that neighbor when a requested filename ends in whitespace.
- Real temporary SQLite fixtures distinguish requested and neighboring rows.
  Plain-path cases fail before the fix; equivalent escaped URI cases pass.
  Require the selected row, retained path and unchanged neighboring contents.
  Native Windows cases reject ambiguous trailing spaces and dots before access
  and require unchanged canonical database bytes.
- Retain existing caller URI-parameter and named/anonymous memory-store tests;
  these are distinct contracts, not duplicates of filesystem path handling.
  Validate full local gates, independent P0–P3 review and exact-commit CI with
  supported downstream app compatibility. No live archives or releases.

## crawlkit: SQLite freshness checks use the requested bundle

- Preserve the literal database path when checking freshness. Trimming selected
  neighboring main/WAL/SHM files, causing both missed and spurious refreshes.
  Reuse the snapshot Windows component check; ambiguous paths return false.
- Replace unrelated freshness assertions embedded in snapshot copying and
  validation tests with dedicated fixed-timestamp cases: older, equal and newer
  main/WAL/SHM files, empty/missing paths and zero cutoff. No sleeps or live
  archives. Preserve all snapshot-specific assertions and fixtures.
- Distinct neighboring timestamps prove literal-path selection in both
  directions for all three files. Native Windows cases reject both filename
  and parent-directory aliases while retaining a fresh canonical-path control.
- Healthy race-enabled suites pass; trimming, ignored sidecars, inclusive cutoff
  and always-fresh-for-valid-input faults all falsely pass the original suite and
  fail the dedicated cases. One stale-bundle row replaces identical older-file
  fixtures; equality and newer-file rows remain distinct for main/WAL/SHM.
- Validate full local gates, independent P0–P3 review, exact-commit CI and
  supported downstream app compatibility. No releases.

## crawlkit: config fixtures and complete metadata assertions

- Consolidate platform fallback fixtures into unset and relative-XDG rows,
  preserving both input contracts. Compare all path fields, including base and
  share directories; retain legacy/new precedence and absolute-XDG cases.
- Fill every runtime config field with a distinct nonzero value and compare the
  complete TOML round-trip result. Require the complete token diagnostic metadata,
  including the environment variable name rather than its value.
- Twenty race-enabled original/final outcomes: healthy suites pass; token-value
  exposure, missing environment name and loss of version/cache/log/share fields
  falsely pass before and fail afterward. Database-path loss and broken relative
  or unset XDG handling fail both versions. Production source is unchanged.
- Validate full local gates, independent P0–P3 review and Linux/Windows PR CI
  with pinned downstream CLI comparison. No live archives or releases.

## crawlkit: consolidate shard rotation fixtures

- Remove the standalone 25-row rotation fixture. The existing two-row generated
  shard case already requires two shards, imports literal rows in full, replace
  and merge modes, and verifies later-shard corruption rolls back the import.
- Move every exported file-metadata assertion into that retained case. Import
  intentionally accepts omitted sizes, so deleting those checks would lose the
  writer's complete-metadata contract. No new helper, fixture or production API.
- Ten race-enabled before/after overlay outcomes preserve detection of disabled
  rotation, absent metadata, zero sizes and missing hashes. The original retained
  case alone accepts zero sizes; the consolidated case rejects them. Healthy
  variants pass and production files remain unchanged.
- Require the full library gate, independent P0–P3 review and exact-head PR CI,
  including Windows and pinned downstream comparison. Synthetic archives only;
  no provider calls or releases.
