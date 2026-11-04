# Test cleanup ledger

Scope: crawlkit, photoscrawl, imsgcrawl, crawl-remote, graincrawl, notcrawl,
discrawl, and gitcrawl. Land narrow PRs; no releases.

Remove tests only with evidence of redundancy, no meaningful assertion, or an
obsolete contract. Keep subprocess helpers and isolated integration fixtures
when their callers verify behavior. Unit tests are not end-to-end proof.

## crawlkit: release notification decisions

- Removed `TestStderrIsTerminalReturnsBool`: discarded the result of a thin
  dependency wrapper and could not detect an incorrect boolean result.
- Extended the existing `ShouldNotify` decision table with interactive success,
  explicit `check-update` suppression, and bare `--json` suppression. The
  positive case detects an implementation that simply always returns false.
- Retained notification writer/error tests and subprocess helpers: they check
  distinct output, error handling, or child-process behavior.
- Validation: workspace-disabled module tidy (unchanged module files), vet,
  and uncached full Go suite; structured autoreview before landing. PR CI runs
  Linux and Windows suites plus the pinned downstream gitcrawl comparison.

## Remaining audit

- crawlkit: review remaining packages, fixture consumers, duplicated coverage,
  flaky timing assumptions, and behavior gaps.
- photoscrawl, imsgcrawl, crawl-remote, graincrawl, notcrawl, discrawl, gitcrawl:
  full test and fixture audit pending; validate each change against its actual
  app behavior using isolated archives and platform-appropriate execution.
