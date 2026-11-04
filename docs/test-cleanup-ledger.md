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
