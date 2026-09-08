# Package guide

`crawlkit` keeps reusable archive mechanics in small Go packages. Downstream apps compose these packages while retaining provider APIs, schemas, authentication, privacy policy, and CLI compatibility.

## Local data

- `config` provides TOML loading, standard config paths, opt-in platform-native runtime directories, legacy-path fallback, and token diagnostics.
- `store` provides SQLite open, read-only, transaction, query, schema-version, FTS5 term, and optimization helpers.
- `state` provides generic crawler cursors and freshness records, including mapped adapters for existing app table layouts.
- `cache` provides read-only local cache files and consistent SQLite database, WAL, and SHM snapshots.

## Portable archives

- `snapshot` exports and imports manifest-based JSONL/Gzip table packs, fingerprints files, plans exact or monotonic incremental imports, and synchronizes managed sidecar trees.
- `backup` writes age-encrypted JSONL/Gzip shards and manifests, manages recipients and identities, lists Git-backed history, and verifies historical restores.
- `mirror` clones, initializes, pulls, commits, and pushes Git-backed archives. It also provides non-mutating fetches, immutable snapshot tags, Git-object reads, and history inspection.

Snapshot exports use `tables/.generations/<32 lowercase hex>/<table>/<ordinal>.jsonl.gz`,
where the ordinal has at least six digits. Manifest fields are unchanged;
planners match these paths to legacy `tables/<table>/<ordinal>.jsonl.gz` IDs.
Unchanged physical shards are reused only after comparing actual bytes.
Old literal readers can consume the paths; old planners may request replacement.
Strict downstream publication validators must explicitly admit this form.

Export holds `.crawlkit-snapshot.lock`, stages closed/synced shards, and publishes
the manifest last. Failures before promotion retain the prior pack. Cleanup
deletes exact prior managed files only; unlisted files and directories are not
recursively owned, and empty generation directories may remain. Post-promotion
cleanup errors return the committed manifest. Readers must coordinate with
pruning; this is not a read lease or a power-loss durability guarantee.

All table reads use one transaction. `ReadTx` optionally borrows a caller's
transaction without ending it, even on error. `FilterTx` runs after the legacy
filter for admitted rows, using that same transaction; it must only read and
must not commit or roll back. Legacy filter closures retain their own database
bindings. The driver's `ReadOnly` option is not an authorization boundary for
trusted callbacks.

Snapshot rows remain v1 JSON objects; no new wire format is enabled. Import
callbacks still receive ordinary numbers as `float64`. Exact integral tokens
outside +/- (2^53-1), including decimal/exponent spellings, become `int64` when
they fit; no `json.Number` escapes to callers. Integers outside signed 64-bit
range, oversized fractional magnitudes, overflow, nonzero underflow, numeric
tokens over 4096 bytes and invalid Unicode fail transactionally. Ordinary
fractional values retain float64 rounding.

V1 export refuses admitted BLOBs and integers outside +/- (2^53-1), including
integral-looking REALs, before publishing the manifest. It also refuses invalid
UTF-8 and numbers the reader cannot represent. Excluded rows do not fail.
Filters retain the legacy BLOB-as-string input and may remove unsupported cells
or replace them with an explicit supported representation, such as prefixed
base64 text; an unchanged implicit BLOB string is not sufficient. Explicit
custom JSON/text encoders own their transformation. The prior pack survives a
refusal. This prevents silent loss, not full BLOB export support or recovery of
binary/text distinctions already lost by old writers.

Encrypted backup writers hold `.crawlkit-backup.lock` through publication and
cleanup. This persistent local marker is not manifest-owned; never unlink it
to release a writer. Publish only exact current/prior manifest paths when an
app stages a backup for Git. Generic `mirror.Commit` remains unchanged.

New encrypted shards and file indexes use unique physical names; legacy
logical names and manifest fields remain readable. The manifest is published
last. A failure before publication preserves the prior pack. A cleanup error
after publication returns the committed manifest and an explicit cleanup
error. Cleanup removes only exact prior manifest-owned objects, not unrelated
files. Callers must coordinate readers with pruning; this is not a concurrent
reader lease or a power-loss durability guarantee.

## Search

- `embed` provides OpenAI-compatible, Ollama, and llama.cpp embedding clients plus probe diagnostics.
- `vector` encodes float32 vectors, validates dimensions, runs exact cosine or optional turbovec-backed search, selects top-k results, and performs reciprocal-rank fusion.

## App contracts

- `control` defines crawler metadata, command manifests, status payloads, contact exports, and database inventories for launchers and automation.
- `output` writes text, JSON, and log-oriented command output.
- `progress` provides progress logging that stays readable in terminals and CI logs.

## Remote archives

- `remote` provides a provider-neutral HTTP client, configuration, query, ingest, authentication, status, SQLite bundle, and protocol-contract types for Worker-fronted archives.

The service boundary is defined in [Remote Contract](remote-contract.md). The Cloudflare Worker and D1 deployment remain outside this module.

## User surfaces

- `scheduler` discovers crawl apps, expands job config, prevents concurrent runs, records JSONL history, and renders or installs native schedules.
- `tui` provides the shared terminal archive explorer: responsive panes, entity and member lists, details, sorting, filtering, mouse actions, and local or remote source status.
- `releasecheck` checks GitHub Releases, caches results, suppresses notices for scripted output, and formats update messages for downstream CLIs.

## Command

- `cmd/crawlctl` is the controller CLI built on `scheduler`. It discovers installed crawl apps through `metadata --json`, runs configured jobs, reports status and logs, and manages periodic schedules.

Browse the exported APIs in the [Go package reference](https://pkg.go.dev/github.com/openclaw/crawlkit).
