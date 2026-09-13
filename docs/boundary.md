# Crawlkit boundary

`crawlkit` owns provider-neutral mechanics shared by at least two crawl apps.
Downstream applications retain their provider APIs, database schemas, privacy
rules, and CLI contracts. Reuse must preserve those contracts.

## Shared mechanics

| Packages | Responsibility |
| --- | --- |
| `config` | Config paths, TOML defaults, platform directories, legacy-path selection, and token-presence diagnostics |
| `store`, `state` | SQLite connection and transaction hygiene, query/FTS helpers, freshness, and adapters for existing cursor tables |
| `snapshot` | Manifest-based JSONL/Gzip packing, import planning and validation, filters, hooks, and managed sidecars |
| `backup` | Age encryption, immutable backup publication, identities, recipients, file indexes, and historical restore |
| `mirror` | Git synchronization, path-scoped commits, unpublished-history preservation, snapshot tags, and object/history reads |
| `embed`, `vector` | Embedding-provider clients, probe diagnostics, vector encoding, exact/optional search, and reciprocal-rank fusion |
| `control`, `output`, `progress` | Metadata/contact DTOs, output formats, and terminal/CI progress logs |
| `cache` | Local file and SQLite bundle captures; callers own source consistency and cache parsing |
| `tui` | Pane layout, grouping, sorting, filtering, menus, detail rendering, refresh, and terminal lifecycle |
| `scheduler`, `cmd/crawlctl` | App discovery, periodic command execution, held run locks, history, and native scheduling |
| `worker` | Bounded continuous processing over application-owned durable queue adapters |
| `releasecheck` | Cached release checks and scripted-output-aware notices |
| `remote` | Hosted-archive HTTP client and additive v1 protocol contracts |

The [package guide](packages.md) describes the current APIs and their storage
contracts. Embedding clients, vector search, mapped state adapters, generic FTS
helpers, snapshot sidecars, and Git history helpers are already implemented;
new work should extend those packages rather than recreate an extraction layer.

## Application responsibilities

Provider-specific logic stays downstream:

- GitHub collection, issue/PR interpretation, clustering, and GitHub-specific
  portable-store schema handling belong to Gitcrawl.
- Discord API/Desktop parsing, DM and wiretap privacy filters, message/member
  schemas, and Discord ranking belong to Discrawl.
- Slack API/Desktop parsing, token scopes, text normalization, channel/thread
  semantics, and analytics belong to Slacrawl.
- Notion page/block/comment/database parsing, Markdown rendering, and Notion
  FTS content belong to Notcrawl.
- WhatsApp, Telegram, Granola, Messages, and Photos ingestion and privacy policy
  belong to their respective applications.

Applications own embedding task selection, input hashes, model configuration,
result storage, and inference prompts. A shared worker owns execution bounds;
its adapters own source revisions, eligibility, transactions, and result fencing.
See [Background workers](background-workers.md).

Worker deployment, D1 migrations, authentication policy, and secrets belong to
`openclaw/crawl-remote`. App query/table allowlists and privacy-aware publishing
remain outside this module. See [Remote Contract](remote-contract.md).

## Compatibility gates

- Keep existing app table shapes. Use `state.ScopedStore`, `state.CursorStore`,
  or their mappings for legacy schemas instead of forcing a shared schema.
- Keep app command names, flags, JSON fields, and deprecated aliases unless an
  intentional downstream compatibility change is documented.
- Keep privacy decisions in caller callbacks. Generic SQL or snapshot helpers
  must not infer whether a row is a private message or channel.
- Use temporary homes, configs, and databases for tests and live proof; never
  touch real crawler stores.
- Use `GOWORK=off` for module and downstream validation so local workspaces do
  not conceal missing published APIs.
- Preserve documented legacy formats and migration paths. A compatibility
  helper is removable only when its downstream contract is retired explicitly.
