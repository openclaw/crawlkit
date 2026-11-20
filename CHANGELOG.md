# Changelog

## Unreleased

- Add `crawlctl preservation upload --resume` to verify and reuse stored chunks without retransmitting their contents.

- Keep mirror file listings working in repositories with submodules by skipping gitlinks that have no blob size or content.

- Preserve exact JSON numbers in remote query and batch-read results instead of rounding large integers and precise decimals through float64.

- Add an explicit retained GitHub snapshot seed profile bound to the original
  file hash, source scope and full-row inventory. Retained snapshots do not
  claim a live change journal or complete upstream acquisition.

- Keep worker retry jitter within the configured maximum without overflowing large retry durations into negative delays.

- Preserve literal SQLite snapshot names so surrounding whitespace does not redirect replacement to a neighboring database bundle; reject ambiguous destination names on Windows.

- Preserve whitespace in FTS table names during optimization so a similarly named table is not modified instead.

- Keep SQLite sidecar cleanup in the selected mirror root when its path contains surrounding whitespace, rather than deleting sidecars from a neighboring directory.

- Keep scoped mirror commits limited to literal file and directory selections, so bracketed filenames do not include matching neighboring files.

- Preserve whitespace in selected mirror commit paths so a similarly named file is not committed instead.

- Add `crawlctl preservation verify` to check all remote backup content without retaining a second local copy.

- Add `crawlctl preservation inspect` to verify a pinned remote backup manifest without downloading chunks or writing files.

- Preserve literal table names during full snapshot imports so names with surrounding whitespace do not clear a different table or retain stale rows.

- Preserve existing file-cache snapshots when timestamps and names collide, including concurrent captures; report the collision instead of overwriting data.

- Let `crawlctl logs -- JOB` display logs for jobs whose names look like command-line flags.

- Keep CLI-installed schedules tied to the selected config and log directory when `--config` is relative.

- Honor `--` when extracting crawlctl global flags so flag-like job names remain literal selections instead of changing CLI behavior.

- Report a failed job when all configured `{repo}` values are blank instead of silently succeeding without running anything.

- Reject Windows Task Scheduler intervals above 1439 minutes during planning instead of producing unsupported minute-mode commands.

- Avoid rebasing and retrying mirror pushes rejected by server policy or authentication; retry only explicit branch-divergence failures.

- Preserve surrounding whitespace in mirror repository paths so initialization and commits do not target a similarly named neighboring repository.

- Preserve symlink traversal when expanding home-relative config paths so reads and writes use the selected file instead of a lexical neighbor.

- Preserve existing immutable mirror tags that point to non-commit objects or appear concurrently during tag creation.

- Verify stored encrypted backup contents before reuse so refreshing unchanged data repairs corrupted or incorrect objects instead of reporting success with an unreadable backup.

## 0.18.0 - 2026-11-15

**Highlights:** Add verified private backup transfers and shared seed APIs, fix scheduler and archive-path handling, and include the Go security fix for GO-2026-6859.

- Add an opt-in private native backup client and `crawlctl preservation` upload
  and download commands. Reuse existing opaque backup bytes, verify immutable
  chunk and complete-file identities, and restore only into a new directory.
  Byte verification does not claim native database restore qualification.

- Add exact file-section inputs to private preservation uploads. Large closed
  files can be transferred in separately pinned volumes without temporary
  copies; section receipts do not claim whole-file restore qualification.

- Use Go 1.27.4 for development and downstream qualification to pick up the standard-library fix for GO-2026-6859.

- Reject malformed `crawlctl logs --tail` counts instead of silently accepting an integer prefix.

- Save discovered executables as absolute paths so relative app selections work from another runner directory.

- Preserve each job's latest text status when history keys collide or runs finish in the same second.

- Keep scheduler due checks scoped to the exact job and repository when colon-containing names share a legacy history key.

- Keep scheduler status scoped to the exact job name when another job or repository history key shares its prefix.

- Allow native independent review in either Codex or OpenClaw while preserving pinned, isolated, read-only review and existing validation gates.

- Recognize supported legacy crawl apps selected by executable path while retaining the selected path in their default jobs.

- Report discovery errors for malformed app metadata or missing manifest IDs while preserving supported legacy fallbacks.

- Reject fractional-second launchd and systemd intervals instead of silently shortening the requested schedules.

- Preserve literal SQLite database paths in control metadata so trailing whitespace does not select a different file for size and modification stats.

- Reject cron paths containing line feeds instead of rendering a broken multiline crontab entry.

- Preserve percent signs in cron executable, config, and log paths instead of truncating the scheduled command.

- Update the SQLite driver to v1.62.0 with its required libc v1.80.1 runtime, x/sys to v0.50.0, and the paired CodeQL actions to v4.39.3; retain the Go 1.27.0 minimum.

- Reject cron intervals that cannot keep a uniform cadence across hour boundaries instead of silently rendering shorter gaps.

- Bound change and seed receipt reads before decoding and reject trailing response data instead of accepting a valid first JSON object.

- Add a closed durable-cluster snapshot profile to the existing GitHub seed
  transport, with a content-bound token and four ordered tables. Existing
  private-report bytes and validation remain unchanged; projection, source
  qualification and receiver policy stay with the applications.

- Add explicit v1/v2 GitHub native-anchor parsing and incremental range validation while keeping existing seed manifests v1-only.

- Add a canonical typed inventory digest with explicit layout identity, native keys, ordered compact row hashes and bounded streaming validation. Existing seed hashes and protocol capabilities remain unchanged.

- Add optional private GitHub seed codecs and exact-byte clients with seven ordered table descriptors, native schema-cookie and inventory-bound anchors, and verified receipts. Native projection, exclusions and baseline eligibility remain downstream.

- Add optional private Slack seed codecs and exact-byte clients with an immutable manifest plan, typed row preservation and verified receipts. Receiver eligibility, durable staging and activation remain downstream.

- Expose the change-batch decoder so receivers and clients share strict wire validation while retaining separate authorization and original-byte hashing.

- Add an optional change-batch client that sends persisted bytes unchanged, preserves typed values and verifies durable receipt identities. Receiver deployment, source projection and outbox ownership remain downstream; the base service contract does not advertise this route.

- Preserve caller-owned query requests when scoping remote batch reads, including when the HTTP request fails.

- Match literal filenames in mirror commit history so bracket and wildcard characters do not include unrelated file changes.

- Preserve literal whitespace in mirror history paths so reads, commit history, and tree selections address the requested filename.

- Validate all backup file shard paths and required prefixes before restoring payloads, preserving existing targets when a later entry has invalid metadata.

- Reject backup JSONL rows above the decoder's 16 MiB line limit before publication, preserving the previous readable backup on failure.

- Avoid creating SQLite database files or directories when opening with an already-canceled context or expired deadline.

- Discard ignored SQLite URI fragments before adding connection options so read-only opens retain filesystem-level write protection.

- Reject snapshot rows above the importer’s 64 MiB line limit before publication, preserving the previous readable pack when export fails.

- Preserve literal filesystem roots when collecting and restoring backup files so trailing whitespace cannot select or overwrite a neighboring directory; reject ambiguous Windows root components.

## 0.17.4 - 2026-11-06

**Highlights:** Preserve literal SQLite archive paths during store access, snapshots, and freshness checks.

- Preserve literal paths in SQLite freshness checks to prevent missed or spurious refreshes caused by neighboring files; reject ambiguous Windows paths.

- Preserve literal filesystem paths when opening SQLite stores so trailing whitespace cannot select or modify a neighboring archive; reject ambiguous trailing spaces and dots in plain Windows filesystem paths.

- Preserve literal source and destination paths in SQLite cache snapshots so trailing spaces cannot select or overwrite a neighboring archive; reject ambiguous trailing spaces and dots in Windows path components.

- Update the indirect x/crypto dependency to remove the retired OpenPGP package flagged by GO-2026-5932; Crawlkit does not import that package.

- Update SQLite to v1.61.1 while retaining its required libc v1.79.0 runtime and the Go 1.27.0 minimum.
- Run pinned downstream compatibility checks alongside library validation to shorten CI feedback without skipping either gate.

- Keep weekly main CodeQL scans instead of repeating analysis on every main push, retain every pull-request scan, and add independently scheduled manual full scans.

## 0.17.3 - 2026-10-23

**Highlights:** Database writes waiting for SQLite locks now honor context cancellation and deadlines.

- Update SQLite to v1.61.0 and its required libc v1.79.0 runtime so writes waiting for database locks honor context cancellation and deadlines; retain the Go 1.27.0 minimum.

## 0.17.2 - 2026-10-20

**Highlights:** Rebuild with Go 1.27.3 to fix the reachable crypto/x509 certificate-validation vulnerability.

### Security

- Build with Go 1.27.3 to fix the reachable crypto/x509 certificate-validation vulnerability GO-2026-6809 while retaining the Go 1.27.0 minimum; require fleet-signed release tags.

## 0.17.1 - 2026-10-20

**Highlights:** Refreshed security analysis and dependency checks, with more reliable Crabbox validation and macOS release preflight.

- Fix Crabbox hydration readiness and shutdown with lease-scoped state files, and allow automatic local hydration by omitting an unsupported explicit cache option.
- Update SQLite's indirect pprof test dependency to the October 16 revision while preserving the required SQLite/libc version pair and Go 1.27.0 minimum.
- Update govulncheck to v1.9.0, CodeQL to v4.39.0, and TruffleHog to v3.99.3 for current security analysis tooling while retaining the Go 1.27.0 minimum.
- Refresh pprof, Go analysis tooling, and the pinned TruffleHog action while retaining the Go 1.27.0 minimum and SQLite's required libc version.
- Move macOS release preflight to Xcode 27, retaining macOS 13 deployment checks and verifying Intel execution through Rosetta.
- Add an explicit release-workflow dry run to validate signed builds without creating a tag or publishing a release; preserve the existing-version guard for real releases.

## 0.17.0 - 2026-10-11

**Highlights:** Stable private copies of live files, plus Go 1.27.2 standard-library security fixes.

- Add `cache.CopyStable` and `cache.CopyStableFiles` with bounded retries, source identity checks, size and space limits, and caller-owned validation for live file copies; protect Windows copies with a private ACL and allow cleanup retries after blocked deletion. Thanks @arimendelow.
- Build and validate with Go 1.27.2 to pick up standard-library security fixes, retain the Go 1.27.0 module minimum, and update the pinned TruffleHog action to v3.99.0. Thanks @dependabot.
- Refresh ANSI rendering, terminal widths, OS support, cryptography, Unicode, pprof, and dead-code analysis dependencies.
- Refresh indirect go-strftime and pprof dependencies while retaining the Go 1.27.0 minimum and the matching SQLite/libc runtime versions.
- Use one shared autoreview installation from `openclaw/agent-skills`; repository entrypoints now receive upstream fixes without copied helpers or test suites.

## 0.16.8 - 2026-10-03

**Highlights:** More resilient SQLite WAL handling and faster large statement binding.

- Update the SQLite driver to v1.60.1 with its required libc v1.77.1 runtime. WAL shared-memory read faults now return an I/O error instead of crashing the process, and binding statements with many parameters no longer scales quadratically. Retain SQLite 3.53.4 and the Go 1.27.0 minimum.
- Refresh the pinned CodeQL action to v4.38.2 and TruffleHog action to v3.97.9.

## 0.16.7 - 2026-10-01

- Preserve newly copied sidecar files after case-only directory renames on case-folding filesystems while pruning stale siblings; keep source spelling in manifests. Thanks @SebTardif.

## 0.16.6 - 2026-09-27

**Highlights:** Much faster embedding decoding and exact vector scoring, with opt-in portable SIMD kernels.

- Speed up 1,536-dimensional vector decode-and-score scans by 5.9× on Apple Silicon, with opt-in `GOEXPERIMENT=simd` kernels adding 1.7× and retaining the scalar fallback.
- Refresh automatic Gitcrawl compatibility qualification across 34 app commits, including fixture-only Git maintenance isolation, and advance its API baseline to published Crawlkit v0.16.5. Keep both complete CLI suites and the manual qualification pins unchanged.

## 0.16.5 - 2026-09-22

**Highlights:** Safer sidecar paths and a faster SQLite runtime.

- Reject overlapping sidecar roots expressed with mixed relative and absolute paths, and reject nested destination directory symlinks before copying so managed files cannot be written outside the target or pruned through an alias.
- Preserve literal whitespace in sidecar directory names and reject ambiguous Windows directory aliases so sync cannot overwrite or prune a different, trimmed destination.
- Update the SQLite driver to v1.59.0 with its required libc v1.75.7 runtime for faster Linux memory operations and fewer callback allocations, retaining the Go 1.27.0 minimum.

## 0.16.4 - 2026-09-20

- Accept reordered snapshot columns during incremental and merge planning when column names are identical and unique, preserving shard replacement and integrity checks.
- Bound scheduler output draining after command exit or cancellation so descendants cannot indefinitely block discovery or hold the refresh-run lock through inherited pipes.
- Reject path-bearing cache snapshot names before publication while preserving literal valid filenames, preventing writes outside the cache directory. Thanks @SebTardif.

## 0.16.3 - 2026-09-13

- Honor configured embedding request timeouts with caller-supplied HTTP clients without mutating those clients; retain shorter client and context deadlines.
- Preserve literal systemd command arguments and executable names; reject executable-path characters that systemd cannot load.
- Preserve file and SQLite capture contents when `MaxFileBytes` is the maximum signed 64-bit value; keep overflow-free bounded reads for growing sources.
- Read and repair long scheduler history records without blocking later runs or status checks.
- Publish changes to zero-row backup counter keys while reusing unchanged encrypted shards.
- Apply private permissions before overwriting existing controller configurations.
- Allow explicit controller configuration paths without a home directory while preserving default and tilde-path validation.
- Refresh indirect test and generator dependencies: pprof, x/exp, x/tools v0.50.0, and modernc/cc v4.29.7 while preserving SQLite's required libc version.
- Run the same validation gates locally and in Linux CI, including formatting, and cancel superseded automatic CI runs.

## 0.16.2 - 2026-09-11

**Highlights:** Updated runtime dependencies and refreshed analysis tooling.

- Refresh operating-system support to x/sys v0.48.0, cryptography to x/crypto v0.57.0, Unicode text handling to x/text v0.42.0, and terminal character widths to go-runewidth v0.0.30 while retaining the Go 1.27.0 minimum. Thanks @dependabot.
- Update deadcode to v0.50.0, govulncheck to v1.8.0, and the pinned CodeQL action to v4.38.0.

## v0.16.1 - 2026-09-11

- Bound worker shutdown when cancellation arrives during retry or completion
  persistence. Remaining claims share one cleanup deadline instead of waiting
  a separate storage timeout per job; rejected batches use the same bound.

## v0.16.0 - 2026-09-11

- Add the generic `worker` runtime for continuous processing over application-owned
  durable queues, with bounded batches, fresh-input priority, cancellation,
  retry/backoff, and safe status snapshots.
- Define revision and lease fencing contracts so background results cannot
  overwrite newer input; applications retain their schemas, eligibility rules,
  transactions, and result persistence.
- Budget leases for slow storage failure cleanup and bound shutdown cleanup for
  the entire batch. SQLite-backed recovery tests and controlled-clock regressions
  cover edits, deletes, expired ownership, duplicate completion, and slow cleanup.

## v0.15.1 - 2026-09-09

- Preserve optional caller-owned warnings in remote ingest manifests and archive
  snapshot metadata without changing the v1 protocol or legacy JSON payloads.

## v0.15.0 - 2026-09-08

- Reject unsupported cascading foreign keys and triggers before generic
  incremental snapshot mutation, preserving skipped and destination-only rows.
  Dependency-aware custom importer callbacks retain their existing contract.
  Classify validated, manifest-selected work before dependency checks and hooks.
- Preserve exact signed integers on snapshot import while retaining ordinary
  numeric callback types. Refuse unsafe v1 integers, BLOBs, invalid text and
  unrepresentable numbers before export promotion instead of silently losing data.
- Stage snapshot exports in immutable generations and publish the manifest
  last, preserving prior packs on failure and logical incremental shard IDs.
  Read tables in one transaction, with additive caller-owned `ReadTx` and
  transaction-aware `FilterTx` options.
- Publish encrypted backup rotations with unique immutable shard and index
  paths under a held writer lock. Preserve the prior pack on failure, restrict
  cleanup to its manifest-owned paths, and report post-commit cleanup errors
  together with the committed manifest.
- Hold a persistent OS lock for the complete scheduler run; repeated cleanup
  cannot remove another owner's lock. Legacy PID locks require stopped-runner
  migration; stop all older runners before upgrading. Mixed old/new runners
  are not supported.
- Require secure, same-origin credential transport for remote redirects and
  provider-less token login and polling, preserving loopback development.
- Redact URL userinfo from Git command failures without changing successful
  archive output.
- Stage SQLite captures before replacement, remove stale destination sidecars,
  and preserve the previous bundle when capture or promotion fails. Raw file
  copies require a quiescent source or an app-owned coherent snapshot; staging
  does not make copies from a live writer transactionally consistent.
- Preserve sidecars when a configured source root is a symlink, and reject
  invalid source roots before creating the destination.
- Publish newly generated backup identities without overwriting a concurrent
  creator's key.
- Preserve unpublished local commits when `PullCurrent` receives an explicit
  remote, and reject divergent history instead of resetting it.
- Honor requested TOML permissions on existing files before writing replacement
  content, including the private default.
- Include Grain, iMessage, Photos, and WeChat in default metadata discovery
  without automatically scheduling source-specific imports.
- Reject newer SQLite schema versions before applying caller schema, release
  transactions when callbacks panic, and isolate unnamed in-memory stores while
  retaining explicitly named shared databases.
- Verify compressed shard hashes, sizes, and decoded row counts before committing full or incremental snapshot imports; bind supplied plans to current manifest metadata, reject inconsistent file paths and modern table row totals, and roll back corrupt imports. Preserve legacy manifests without per-file metadata; their nonnegative table row totals remain advisory, including positive values.

## v0.14.9 - 2026-09-05

**Highlights:** Safer snapshot shard paths and recovery from interrupted scheduler-history writes.

- Reject absolute and parent-traversing snapshot shard paths in full and incremental imports while preserving current-directory roots and literal filesystem names. Thanks @SebTardif.
- Recover truncated scheduler history tails after interrupted writes while preserving valid final records without a newline and reporting write or cleanup failures. Thanks @SebTardif.
- Update SQLite to v1.58.0 with its required libc v1.75.6 runtime, refresh x/crypto and go-runewidth, and prefer Go 1.27.1 while retaining the Go 1.27.0 minimum.
- Refresh the pinned TruffleHog secret-scanning action to v3.97.4 and GitHub Script to v9.0.0. Thanks @dependabot.

## v0.14.8 - 2026-08-30

- Default release checks to a 30-second HTTP timeout so unresponsive servers cannot hang checks indefinitely.
- Reject release dispatches for existing tags or releases before rebuilding artifacts, and serialize dispatch checks through publication.
- Require Go 1.27.0, update SQLite to v1.57.0 and supporting Go dependencies, and refresh deadcode, govulncheck, CodeQL, and TruffleHog validation tools.
- Update age to v1.3.2 for encrypted-backup fixes and input hardening.

## v0.14.7 - 2026-08-14

- Update `modernc.org/sqlite` to v1.56.0 for the SQLite 3.53.3 journal-rollback corruption fix and regenerated platform bindings.
- Require Go 1.26.6 for the latest standard-library security fixes.
- Refresh the pinned CodeQL init and analysis actions to v4.37.6.

## v0.14.6 - 2026-08-06

- Publish the module as an SSH-signed tag without a GitHub Release or attached
  `crawlctl` artifacts; the unified pipeline still needs its signing secrets
  provisioned before it can attach binaries.
- Fix the TUI filter so typing `q` appends to the query instead of quitting; `ctrl+c`/`ctrl+d` still quit while filtering.
- Fix TUI filter backspace to remove one rune instead of one byte, keeping CJK and emoji queries valid UTF-8.
- Guard `startRefresh` against overlapping runs so slow refreshes no longer stack goroutines; a manual refresh reports "Refresh already in progress".
- Rewrite the README to the shared project standard and move the full package inventory to `docs/packages.md`.
- Prepare the unified release pipeline for future Developer ID-signed and
  notarized `crawlctl` macOS archives and static Linux archives.

## v0.14.5 - 2026-08-02

- Publish the module as an SSH-signed tag without a GitHub Release or attached
  `crawlctl` artifacts; v0.14.4 remains the latest release with binary assets.
- Update Go dependencies and validation tools, including SQLite v1.55.0, `modernc.org/libc` v1.74.4, `go-colorful` v1.4.1, deadcode v0.48.0, and govulncheck v1.6.0; document caller-provided `file:` URI parameter pass-through.
- Refresh the pinned TruffleHog and CodeQL security actions to v3.96.0 and v4.37.4.
- Update the stale repository automation to the immutable v11.0.0 action revision.
- Raise the brokered AWS Crabbox root volume to 400 GB to match the current developer snapshot minimum.

## v0.14.4 - 2026-07-26

### Final attached CLI artifacts

- Publish the final GitHub release with attached `crawlctl` artifacts for macOS and Linux.

### Maintenance

- Update Go dependencies, including `go-runewidth` v0.0.27 and `x/crypto` v0.54.0, and refresh Actions Checkout to v7.0.1 and CodeQL Action to v4.37.3.
- Standardize local build and check commands under the shared crawler Makefile contract.

## v0.14.3 - 2026-07-17

### Highlights

- Update the bundled SQLite engine to v3.53.3 through `modernc.org/sqlite` v1.54.0.

### Maintenance

- Update terminal detection to `go-isatty` v0.0.23 and refresh CI security tooling.

## v0.14.2 - 2026-07-12

- Add a snapshot-scoped publisher status client helper so resumable publishers
  verify the exact immutable candidate instead of trusting an unrelated
  completed candidate from the unscoped status route, and reject successful
  responses that omit the snapshot or return a different snapshot, app, or
  archive.
- Add a 64 MiB mutable SQLite bundle default and preserve exact request content
  lengths for bounded remote uploads. The shipped
  `DefaultSQLiteBundleChunkSize` constant and immutable snapshot default remain
  256 MiB for caller and retry compatibility, including explicitly configured
  legacy mutable bundles. Mutable bundles retain their legacy part-count and
  aggregate-size, chunk-size, and object-size compatibility, while immutable
  snapshots enforce the negotiated 256 MiB part, 4 GiB object, 512 MiB
  compressed, and eight-part bounds. Bundle construction now rejects empty
  sources, removes partial temp artifacts on failure, and rejects source
  identity/content drift. Immutable uploads use private validated part
  snapshots and verify compressed and decompressed digests before any remote
  write. Every file-backed read is capped at its declared size plus one byte so
  concurrent same-inode growth is detected without consuming an unbounded tail.
  Mutable uploads retain bounded sequential file handling: `Size: -1` uses the
  statted file size for local verification while preserving unknown-length
  transport, and part digests remain optional but are verified when supplied.
  Minimal legacy manifests, index-keyed part matching, and no duplicate
  temporary staging are also preserved; tiny chunk sizes avoid caller-sized
  part-slice preallocation. Manifests use crawl-remote's
  deterministic two-space persisted JSON representation and are size-preflighted
  before full encoding: snapshots are capped at 64 KiB while legacy mutable
  manifests retain the service-compatible 1 MiB ceiling.

## v0.14.1 - 2026-07-12

- Add a publisher-authenticated archive status route and client helper so
  interrupted snapshot publication can resume without reader credentials.

## v0.14.0 - 2026-07-12

- Add immutable SQLite snapshot bundle manifests with deterministic source and representation digests, digest-scoped compressed objects and chunks, and explicit snapshot publishing helpers while preserving the legacy `current.*` bundle layout.
- Expose immutable snapshot capabilities separately from the currently served archive capabilities so publishers can safely resume staged profiles before cutover.

## v0.13.4 - 2026-07-09

- Require Go 1.26.5 to include the standard-library TLS security fixes.

## v0.13.3 - 2026-07-06

- Update the TOML parser to v2.4.3 for recursion, panic, and deeply nested input hardening.
- Keep paired CodeQL actions on one version during dependency updates and update TruffleHog to v3.95.8.

## v0.13.2 - 2026-07-02

- Add explicit monotonic snapshot merge planning and import-impact reporting so cache consumers can apply changed shards without silently replacing local rows, while exact mirrors retain replacement semantics.

## v0.13.1 - 2026-06-23

- Harden crawlkit scheduler, output, release-check, vector ranking, and CI
  workflow edges found by `clawpatch`.
- Expose remote ingest reset progress so crawl publishers can drain large
  Cloudflare D1 table replacements before sending row batches.

## v0.13.0 - 2026-06-19

- Add content-deduplicated encrypted file bundles with safe current and historical restore for crawler backups.
- Add shared SQL LIKE literal escaping for crawler search queries.
- Preserve Unicode combining marks in tokenized FTS5 queries.
- Add a safe tokenized FTS5 query helper for arbitrary user search input.
- Preserve unrelated tracked mirror edits across write synchronization rebases.
- Retry atomic branch-and-tag snapshot pushes after rebasing and retargeting unpublished tags.
- Update Go to 1.26.4 for current standard-library security fixes and add race and vulnerability CI gates.
- Preserve unpublished local mirror branches during pulls, clone the requested remote branch, and support caller-selected private repository directory modes.
- Allow managed sidecar trees to prune selected generated files while preserving unrelated files.
- Add non-mutating archive tag validation and rebase-based write synchronization for durable multi-machine backup retries.
- Add reusable Git snapshot history/tag/ref restoration, SQLite bundle snapshots, managed snapshot sidecars, mapped sync-state adapters, FTS5 helpers, and the shared contact-export contract; refresh stable dependencies.

## v0.12.2 - 2026-06-17

- Fix SQLite file URI generation for absolute Windows paths while preserving
  reserved-character escaping, and add Windows CI coverage.

## v0.12.1 - 2026-06-17

- Harden crawlkit maintenance surfaces found by `clawpatch`: safer `crawlctl`
  log tailing, GOWORK-isolated validation, atomic backup/cache/store writes,
  safer snapshot incremental imports, scoped state schemas, scheduler lock and
  interval handling, release/version checks, remote bearer-token transport
  validation, and path-scoped mirror commits.

## v0.12.0 - 2026-06-10

- Add shared `vector.Search` backends, including the existing exact cosine
  scorer and an optional Python `turbovec` bridge for downstream semantic
  search over dimensions divisible by 8 up to 8,192. Thanks @vincentkoc.
- Add a `remote.Client.UploadSQLite` helper, SQLite object metadata in remote
  status payloads, and contract metadata for publisher-side raw SQLite archive
  uploads.
- Add gzip-compressed SQLite bundle manifests, chunk builders, and upload
  helpers for R2-backed remote archive bootstrap/fallback data.

## v0.9.0 - 2026-05-28

- Add explicit `remote` protocol contract metadata and a public
  `Client.Contract` helper so Worker-fronted archive services can prove their
  supported routes, auth roles, query names, and ingest tables without coupling
  Cloudflare deployment state to the Go module.

## v0.8.0 - 2026-05-27

- Add provider-neutral `remote` client/config/status contracts for
  Worker-fronted cloud archives, including authenticated query, batch-read,
  ingest, archive listing, and GitHub login helper calls.
- Add remote archive fields to `control.Status` and database inventories so
  downstream apps can report D1/Worker storage without pretending it is a local
  SQLite path.

## v0.7.0 - 2026-05-22

- Add `crawlctl`, a cross-platform controller for discovering crawl apps, running configured refresh jobs, recording JSONL run history, tailing logs, and installing OS-native schedules through launchd, systemd, Windows Task Scheduler, or cron output.
- Add the `scheduler` package for crawl app discovery, legacy app adapters, job expansion, single-process run locking, native schedule rendering, and run-history helpers.

## v0.6.0 - 2026-05-17

- Add `releasecheck` helpers for cached GitHub release checks and safe stderr
  update notices in downstream crawl app CLIs.

## v0.5.3 - 2026-05-17

- Add OpenAI-compatible embedding dimensions, raw API-key injection, user-agent
  forwarding, response headers on HTTP errors, and float64 vectors for
  downstream stores that need exact JSON embedding values.

## v0.5.2 - 2026-05-11

- Add opt-in platform-native config/runtime paths to `config.App`, with
  XDG/macOS defaults and migration-safe legacy path fallback.

## v0.5.1 - 2026-05-08

- Add reusable `backup` helpers for age identities, encrypted JSONL/Gzip shards,
  manifests, recipient tracking, shard hash verification, and stale shard
  cleanup.

- Add reusable `embed` providers for OpenAI, OpenAI-compatible endpoints,
  Ollama, and llama.cpp, including probe diagnostics and rate-limit errors.
- Add reusable `vector` helpers for float32 blobs, dimension validation,
  cosine similarity, top-k sorting, and reciprocal-rank fusion.

## v0.4.2 - 2026-05-08

- Add snapshot file fingerprints and an incremental import planner/executor so downstream apps can import changed JSONL/Gzip shards without deleting every table.
- Move the module path to `github.com/openclaw/crawlkit`.
- Bump routine Go module dependencies.

## v0.4.1 - 2026-05-06

- Add GitHub Sponsors funding metadata.
- Add crawlkit agent guidance for shared-library maintenance.
- Document downstream adoption status for `gitcrawl`, `discrawl`, `slacrawl`,
  and `notcrawl`, including the app-owned provider/auth/privacy boundary.
- Document the `crawlkit` versus crawl-app boundary for embeddings, search,
  inference, sync state, snapshots, SQLite, and git mirrors.
- Add safer `mirror` helpers for origin updates, existing-origin pulls,
  path-scoped commits, and portable SQLite sidecar cleanup.
- Add `state.ScopedStore` and `state.CursorStore` adapters for legacy sync
  state table shapes used by downstream apps.

## v0.4.0 - 2026-05-05

- Initial `crawlkit` module scaffold.
- Add the `control` package to the public package inventory for app metadata,
  command manifests, status payloads, and database inventory.
- Add `tui`, a shared Bubble Tea terminal archive browser used by the crawl apps for consistent `tui` command behavior.
- Improve `tui` rows with compact column rendering, pane-specific scrolling, and full-height pane borders.
- Tune `tui` pane colors and mouse-wheel buffering to better match the `gitcrawl` terminal browser feel.
- Add shared `tui` explorer controls: mouse row selection, pane-aware right-click menus, help/sort menus, and stable sorting by time/title/kind/scope/container/author.
- Align `tui` pane chrome with `gitcrawl`: wide three-column layout, split/stacked resize modes, focused pane titles, compact row headers, click-to-sort headers, and floating right-click menus.
- Make the shared `tui` explorer group-aware: left pane now shows channels/people or document parents, middle pane shows group members, and right pane shows detail/thread content.
- Polish shared `tui` detail panes with chat-style transcript rendering, document location/preview sections, chronological chat member ordering, and compact columns in narrower tmux panes.
- Fix shared `tui` pane-specific header sorting, scope sorting, and stable detail metadata labels across crawl apps.
- Render shared `tui` parent/member panes with gitcrawl-style table columns, row styling, pane-local header sorting, and a 24-line minimum layout.
- Use a gitcrawl-style viewport for `tui` detail panes so long threads and document previews scroll cleanly inside the focused pane.
- Render `tui` detail content with gitcrawl-style sections, rules, markdown-ish wrapping, and pane-width-aware chat/document previews.
- Add gitcrawl-style pane-specific sorting so group rows and member/message rows keep independent sort modes from headers or the sort menu.
- Add a gitcrawl-style `d` detail-mode toggle so noisy metadata can collapse behind compact chat/document previews.
- Add a shared `v` group-view toggle so chat archives can pivot left pane by channel, person, or thread, and document archives by parent, database, or workspace.
- Add gitcrawl-style selected-row actions for opening URLs and copying URLs, titles, or rendered detail text from the TUI action menu.
- Add gitcrawl-style `a` action-menu shortcut, context-specific action menu titles, and double-click-to-open selected archive rows.
- Add gitcrawl-style body-link actions for opening or copying links found in selected chat messages and document previews.
- Refine the shared TUI toward `gitcrawl` parity with semantic pane titles, compact readable detail by default, bounded document previews, and conversation-window fallback for unthreaded chat messages.
- Match more of the `gitcrawl` interaction model in the shared TUI: action menus now keep the detail pane chrome, and `s`/`m` cycle group/member sorting while `S` opens the full sort menu.
- Preserve useful `tui` table columns in 120-column tmux panes so group rows keep date/age and member rows keep time/age/who/title instead of collapsing to title-only lists.
- Start chat and document `tui` detail panes in compact readable mode, while keeping `d` as the compact/full detail toggle for metadata.
- Trim redundant chat member columns so message panes prioritize time, age, author, and title instead of repeating the selected channel/kind on every row.
- Default single-channel chat archives to a people/group view so the left pane stays useful for Discord and Slack data.
- Match `gitcrawl` TUI resize breakpoints: 140+ columns use three panes, 100-139 uses top split plus full-width detail, and narrow terminals stack.
- Include selected chat row URLs in full detail properties so message panes expose the same useful link context as `gitcrawl`.
- Align shared TUI pane accents, header, footer, and selected-row colors with the current `gitcrawl` terminal browser palette.
- Match `gitcrawl` help behavior by rendering `?` help inside the detail pane instead of replacing the screen with a menu.
- Keep medium-width group panes focused on count/date/age/name instead of repeating the group kind on every row.
- Bring shared TUI detail and sort behavior closer to `gitcrawl`: archive groups can sort by count or time from headers, selected chat messages render before surrounding conversation context, document previews appear before metadata, and detail fields use `key: value` labels.
- Keep split-width member tables readable by rendering compact dates instead of truncated ISO timestamps.
- Open chat and document TUIs on the densest group by default, matching `gitcrawl`'s count-first startup so the middle pane is populated immediately.
- Prioritize gitcrawl-style footer muscle-memory controls in compact tmux panes before app-specific extras.
- Render selected chat message bodies with the same transcript marker as their speaker line so detail panes read more like chat.
- Show chat reply counts in detail metadata when apps provide thread/reply counts.
- Force the Bubble Tea program to shut down on terminal signals so interrupted TUIs restore terminal modes and do not leave orphaned tmux panes.
- Rename the public package nouns to `config`, `store`, `snapshot`, `mirror`, `state`, `output`, `tui`, and `cache`.
- Show pane-specific action menu titles in the TUI status/footer instead of leaking generic row/context labels.
- Add `snapshot.ImportOptions.Progress` so apps can report table/file-level import progress in CI logs.
- Match `gitcrawl` action-menu muscle memory: keyboard `a` opens a general detail-pane menu, right-click keeps pane-scoped floating menus, and `q`/Esc close menus.
- Keep chat/document TUI groups scoped by workspace/server so same-named channels, people, parents, or databases do not collapse into one misleading pane row.
- Add a chat member relation column so Slack/Discord message panes distinguish normal messages, thread roots, and replies at a glance.
- Add gitcrawl-style non-selectable section rows to the TUI context pane, with chronological date breaks for chat and page/database sections for document archives.
