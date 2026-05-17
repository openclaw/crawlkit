# crawlkit boundary

`crawlkit` is the shared mechanics layer for local-first crawler archives. It
should make each crawl app smaller and more uniform without turning into a
generic Slack, Discord, Notion, or GitHub crawler.

The rule is simple: move behavior into `crawlkit` only when it is provider
neutral, reusable by at least two apps, and can preserve the app's existing
database and CLI contracts. Keep provider schemas, auth, API clients, cache
parsers, and product-specific ranking in the apps.

## adoption status

| app | branch | crawlkit usage | still app-owned |
| --- | --- | --- | --- |
| `gitcrawl` | `main` | config paths, SQLite openers, command/control metadata, status inventory, and the reference TUI/control contract | GitHub API sync, `gh` shim behavior, embeddings, clustering, inference, portable-store schema pruning, and the richer cluster TUI |
| `discrawl` | `main` | config/status/control, snapshot packing/import, git mirror mechanics, sync-state adapters, output helpers, and shared chat TUI | Discord bot API, desktop wiretap parsing, DM privacy filters, Discord schema, FTS/ranking, embeddings, and analytics |
| `slacrawl` | `feat/use-crawlkit` | config/status/control, snapshot packing/import, git mirror mechanics, state helpers, output helpers, and shared chat TUI | Slack API/Desktop parsing, token scopes, Slack schema, Slack text normalization, channel/thread semantics, and analytics |
| `notcrawl` | `feat/use-crawlkit` | config/status/control, snapshot packing/import, git mirror mechanics, output helpers, and shared document TUI | Notion API/Desktop parsing, Markdown rendering, page/comment/database schema, Notion FTS body construction, and data-source compatibility |

## owns

`crawlkit` should own these surfaces:

- Config paths, TOML loading defaults, opt-in platform-native runtime
  directories, migration-safe legacy path fallback, and token diagnostics that
  are the same across apps.
- SQLite connection hygiene: read-only opens, busy timeouts, WAL pragmas,
  schema-version checks, transactions, safe identifier quoting, and generic
  query helpers.
- Snapshot packing: manifest format, JSONL/Gzip shards, table filters,
  per-file fingerprints, import progress, incremental import planning,
  sidecar registration, backward-compatible manifest reads, and import
  callbacks.
- Git mirror mechanics: clone/init, pull, origin management, path-scoped
  commits, push retry behavior, and portable SQLite checkout cleanup.
- Sync freshness semantics: cursor/freshness records, stale checks, manifest
  import state, and adapters for legacy table shapes.
- Embedding provider clients and vector math once extracted: OpenAI-compatible,
  Ollama, llama.cpp, probe diagnostics, cosine search, top-k selection,
  reciprocal-rank fusion, vector encoding, and dimension validation.
- FTS utilities that do not know app schemas: query escaping, snippets,
  rebuild/optimize helpers, deferred refresh orchestration, and progress logs.
- Terminal archive browsing primitives: pane layout, sorting, focus, mouse
  actions, menus, detail rendering primitives, and local/remote status chrome.
- Provider-neutral release checks for crawl app binaries: latest GitHub release
  fetches, local check caching, scripted-output suppression, and text notice
  formatting. Apps still own their command names, version variables, and install
  hints.
- Safe read-only desktop-cache snapshot helpers. The provider-specific parsing
  of those snapshots stays in the apps.

## does not own

`crawlkit` should not own these surfaces:

- Slack, Discord, Notion, GitHub, or future provider API clients.
- App-specific auth flows, token scopes, rate-limit policy, and provider
  object normalization.
- App database schemas for messages, pages, threads, issues, members, blocks,
  comments, channels, guilds, or workspaces.
- Provider desktop-cache parsing such as Slack LevelDB records, Discord cache
  rows, or Notion SQLite object trees.
- App-specific FTS bodies and ranking, such as Notion display-tree ordering,
  Slack mention normalization, Discord member search, and GitHub issue/PR
  syntax.
- Summarization, clustering, triage inference, or prompts until the same
  behavior exists in more than one app.
- App CLI command contracts. Shared helpers can format JSON/text/log output,
  but the apps decide command names, flags, backward-compatible aliases, and
  deprecation behavior.

## current app seams

| app | embeddings/search/inference | sync state | snapshot, sqlite, remote |
| --- | --- | --- | --- |
| `gitcrawl` | Has the richest inference path: OpenAI-only embeddings, local thread vectors, exact cosine neighbors, durable clusters, and GitHub thread/document FTS. The vector math and portable embedding client should move to `crawlkit`; GitHub thread task construction, clustering, and prompts stay app-owned. | Uses app-owned repo sync and portable metadata. Do not force it into the shared `sync_state` table. | Has the most mature portable-store git behavior: clone/pull, dirty checkout recovery, SQLite sidecar cleanup, and portable payload pruning. The generic git/SQLite checkout pieces belong in `mirror`; GitHub portable schema pruning stays app-owned. |
| `discrawl` | Has the best reusable embedding provider surface: OpenAI, OpenAI-compatible, Ollama, llama.cpp, probe checks, float32 blobs, semantic search, hybrid search, and RRF. Provider clients, vector encoding, cosine, top-k, and RRF should be extracted. Discord message/member FTS and privacy boundaries stay app-owned. | Uses a single `scope -> cursor` table with local-only scopes such as `wiretap:*`. Shared state should adapt to this shape, not migrate it. | Uses `snapshot` and `mirror`, with important app filters for DMs and local-only sync state. Embedding bundles are sidecars today; generic sidecar/binary-vector mechanics should move to `snapshot`, while DM exclusion remains in `discrawl`. |
| `slacrawl` | Has Slack FTS and Slack text/mention normalization. Embeddings are only reserved placeholders. Slack normalization and message FTS stay app-owned. | Closest to `crawlkit/state`: `source_name`, `entity_type`, `entity_id`, `value`, `updated_at`. It is the first app that can consume shared state directly. | Uses `snapshot` and `mirror` cleanly. Its remaining share logic is mostly table lists, search-index rebuilds, and import freshness. |
| `notcrawl` | Has page/comment FTS, display-tree page bodies, deferred FTS refresh, and maintain/rebuild commands. No embeddings yet. Deferred FTS orchestration can become shared; Notion page/comment FTS content stays app-owned. | Uses `source`, `entity_type`, `entity_id`, `cursor`, `synced_at`. Shared state needs column mapping or adapters before this can be de-duped safely. | Still carries custom manifest, JSONL/Gzip, Markdown sidecars, generated-path commits, and origin update behavior. The snapshot sidecar model and mirror path-scoped commit/origin helpers should let this converge without changing the Notion DB schema. |

## extraction order

1. Harden `mirror` first.
   Add origin update semantics for existing checkouts, path-scoped commits so
   publish never stages unrelated files, existing-origin pull for update flows,
   and portable SQLite sidecar cleanup. This is the lowest-risk de-dupe because
   every app already shells out to git in similar ways.

2. Expand `snapshot` sidecars.
   Keep table export/import generic, but add first-class sidecar/bundle helpers
   for Markdown pages and embedding JSONL/Gzip bundles. Apps still provide
   filters, table lists, delete callbacks, FTS rebuild callbacks, and privacy
   rules.

3. Add state adapters instead of one forced schema.
   Keep the current source/entity/value schema as the canonical new shape, but
   add adapters for `scope -> cursor` and `source/entity/cursor/synced_at`
   stores. `state.ScopedStore` and `state.CursorStore` cover those legacy
   shapes so apps can share freshness and stale checks without risky
   migrations.

4. Extract embeddings and vector search.
   Start from `discrawl/internal/embed` for provider clients and from
   `gitcrawl/internal/vector` plus discrawl search helpers for cosine, top-k,
   vector encoding, and reciprocal-rank fusion. Apps keep task selection,
   content hashing policy, provider config placement, and result persistence.

5. Add generic FTS helpers.
   Provide query escaping, snippets, rebuild/optimize wrappers, deferred refresh
   orchestration, and progress logging. Do not move entity-specific FTS schemas
   or ranking into `crawlkit`.

6. Keep inference app-owned until there are two implementations.
   `gitcrawl` clustering and summary-oriented work should not be generalized
   yet. Extract only the provider/vector primitives it shares with chat/document
   crawlers.

## compatibility gates

Every extraction must keep these constraints:

- Do not change existing app table shapes unless the app migration is explicitly
  backward-compatible and tested against old fixtures.
- Do not change app command names, flags, JSON shape, or deprecated aliases
  unless the downstream app changelog calls it out.
- Do not touch live stores during tests. Use temp homes, temp configs, and temp
  SQLite files.
- Use `GOWORK=off` when proving the public `crawlkit` API so local workspaces
  do not hide missing release tags.
- Keep privacy filters in the app layer. `crawlkit` can run a filter callback;
  it should not know what a Discord DM or Slack private channel means.
