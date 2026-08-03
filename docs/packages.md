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
