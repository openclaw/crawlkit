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
