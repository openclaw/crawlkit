# Test cleanup ledger

This records the crawlkit portion of the crawl-app test cleanup campaign.
Land narrow PRs; no releases. Other repositories keep their own audit evidence.

## Bounded coverage

Read all 53 Go test files (including platform tests and benchmarks) and all
three Node test files. Inspect fixture consumers and owning code before removing
coverage or fixing a demonstrated gap. Inline fixtures and temporary archives
are included; the tracked inventory contains no `testdata/` or fixture-named files.

Remove cases only with evidence of redundancy, a vacuous assertion or an obsolete
contract. Retain distinct behavior even when setup looks similar. This bounded
read is complete; it does not certify every product path or provider integration.

## Landed changes

Each linked PR owns its exact diff, fault probes, review and validation evidence.

| PR | Area | Result |
| --- | --- | --- |
| [176](https://github.com/openclaw/crawlkit/pull/176) | Notifications | Replace a discarded terminal result; cover positive and suppressed notifications. |
| [182](https://github.com/openclaw/crawlkit/pull/182) | Vectors | Remove duplicate round-trip coverage and NaN-unsafe assertion helpers; retain bit and rank oracles. |
| [184](https://github.com/openclaw/crawlkit/pull/184) | Remote HTTP fixtures | Report callback failures with HTTP errors instead of aborting connections via FailNow. |
| [185](https://github.com/openclaw/crawlkit/pull/185) | Provider/update fixtures | Apply the same callback correction while preserving payload, cache and root-test assertions. |
| [186](https://github.com/openclaw/crawlkit/pull/186) | Manifest fixtures | Return strict decoder errors to callers; retain unknown-field and trailing-JSON rejection. |
| [188](https://github.com/openclaw/crawlkit/pull/188) | Worker fixtures | Bound phase waits and let cancelled setup stop its handler; retain queue and revision checks. |
| [189](https://github.com/openclaw/crawlkit/pull/189) | Vector subprocess helper | Do not conceal a panic with a deferred success exit. |
| [190](https://github.com/openclaw/crawlkit/pull/190) | TUI ordering | Require rendered content before comparing its positions. |
| [191](https://github.com/openclaw/crawlkit/pull/191) | TUI preview | Require all 18 compact preview lines, rather than only a ceiling. |
| [193](https://github.com/openclaw/crawlkit/pull/193) | Worker cancellation | A failed token read cannot prove claim release. |
| [194](https://github.com/openclaw/crawlkit/pull/194) | Backup cleanup | Require kept ciphertext to remain byte-identical while stale shards are removed. |
| [195](https://github.com/openclaw/crawlkit/pull/195) | Cache names | Remove a duplicate escape fixture; retain the stronger outside-file and cache-debris checks. |
| [196](https://github.com/openclaw/crawlkit/pull/196) | SQLite snapshots | Preserve literal source/destination paths and neighboring bytes; reject ambiguous Windows aliases. |
| [197](https://github.com/openclaw/crawlkit/pull/197) | SQLite stores | Preserve literal writable/read-only paths, URI and memory contracts; reject Windows aliases. |
| [198](https://github.com/openclaw/crawlkit/pull/198) | SQLite freshness | Inspect the requested main/WAL/SHM paths; use fixed cutoff and neighboring-file cases. |
| [199](https://github.com/openclaw/crawlkit/pull/199) | Config | Share fallback setup and compare complete paths, runtime fields and token metadata. |
| [201](https://github.com/openclaw/crawlkit/pull/201) | Snapshot rotation | Fold the 25-row fixture into the retained rotating-shard case; preserve file metadata checks. |
| [202](https://github.com/openclaw/crawlkit/pull/202) | Snapshot filters | Check literal identities, bodies and sync state instead of counts alone. |
| [203](https://github.com/openclaw/crawlkit/pull/203) | Snapshot imports | Fold legacy-file and stripped-metadata fixtures into existing import matrices. |
| [206](https://github.com/openclaw/crawlkit/pull/206) | State | Share default-constructor setup; read all records after all schema writes. |
| [207](https://github.com/openclaw/crawlkit/pull/207) | Backup file roots | Preserve literal collection/restore roots and neighbors; reject Windows aliases. |
| [208](https://github.com/openclaw/crawlkit/pull/208) | Backup validation | Require the intended namespace/path diagnostic, not an unrelated filesystem error. |
| [209](https://github.com/openclaw/crawlkit/pull/209) | Downstream smoke contract | Freeze all six commands and complete binary/config invocations independently of production. |
| [210](https://github.com/openclaw/crawlkit/pull/210) | Mirror validation | Seed a local commit and require intended ref, path, limit and tag errors. |
| [211](https://github.com/openclaw/crawlkit/pull/211) | Cache copies | Share exact-limit/retry fixtures; use an independent literal five-attempt default. |
| [212](https://github.com/openclaw/crawlkit/pull/212) | Scheduler | Retain byte-prefix recovery and public Run coverage; fold explicit history placement into path checks. |
| [213](https://github.com/openclaw/crawlkit/pull/213) | Legacy uploads | Share three mutable transport fixtures, strengthen their common assertions and remove a no-op TestMain. |

## Closeout cleanup

- Fold the historical v0.14.7 release fixture into the existing public/draft
  rows for that same version. Preserve the rebuild diagnostic and three-lookups
  assertion. Eight paired Node outcomes retain rejection, diagnostic and lookup
  controls; temporary test copies are removed and production bytes stay unchanged.
- Remove the worker peak counter and budgetQueue.jobs field: both were written
  but never inspected. Preserve batching, completion and lease-budget assertions.
- Replace repeated validation prose and obsolete pending-audit notes with this
  index. Keep every landed change represented; production stays unchanged.

## Retained contracts

- Snapshot format/version/metadata/selection matrices, corruption rollback,
  encrypted publication and owned-versus-unowned cleanup cover distinct paths.
- SQLite WAL/SHM, URI/memory semantics, literal Unix names and native Windows
  alias/ACL cases differ. Stable-copy identity, growth, cancellation and cleanup
  retry cases remain separate.
- Worker durable leases/revisions and slow-storage adapters protect different
  boundaries. Scheduler public Run, long records, corrupt/truncated tails,
  valid EOF and persistence failures remain.
- TUI startup shortcuts before window sizing, sized rendering, mouse menus,
  focused-pane actions and cancellation/layout states remain distinct.
- Mutable/immutable uploads, direct-part transport, independent wire schemas,
  staging, bounds and manifest preflight remain distinct synthetic contracts.
- Vector bit/arithmetic/rank oracles, SIMD/scalar checks and opt-in external
  bridge tests differ. Provider, lock, config, output and control tests retain
  their separate success, failure and ownership contracts.

## Verification and limits

Use workspace-disabled tidy with unchanged module files, vet and uncached Go
tests. The repository gate also covers formatting, analysis, vulnerabilities,
race tests and Node scripts. Independent P0–P3 review and exact-head Linux,
native Windows and pinned gitcrawl CI qualify each changed diff. Production
path fixes also require isolated downstream checks; consult their linked PRs
for the actual scope and results.

Fault overlays prove targeted assertions; mocked HTTP and command tests remain
synthetic. This ledger claims no live archive, live provider, installed-app or
whole-system E2E proof. No release or deployment is part of this campaign.
