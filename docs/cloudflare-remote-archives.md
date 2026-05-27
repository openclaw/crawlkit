# Cloudflare remote archives

Status: implementation spec with local Worker/D1 prototype.

This spec covers an additive remote archive option for `crawlkit`,
`openclaw/gitcrawl`, and `openclaw/discrawl`.

The target is a Cloudflare-hosted SQLite archive backed by D1, accessed through
an OpenClaw Worker with GitHub organization authentication. Existing local
SQLite databases and Git-backed portable stores must keep working exactly as
they do today.

## Recommendation

Build the remote option as a Worker-fronted D1 service, not as direct client
access to the Cloudflare D1 REST API.

The CLI should never require a user's Cloudflare account token. The Worker owns
the D1 binding, GitHub org/team authorization, query allowlists, ingest policy,
and audit trail. `crawlkit` should provide the reusable remote archive HTTP
client, token storage helpers, status/query result types, and test harness.
`gitcrawl` and `discrawl` should keep their provider schemas, provider syncers,
privacy filters, and command contracts.

This is a hosted SQLite archive, not a downloadable SQLite-file mirror. D1 is
the native Cloudflare SQLite database layer; readers interact with it through
Worker APIs and receive rows/pages, not `.db`, WAL, SHM, or Git snapshot files.

## Goals

- Add a fully remote read mode where normal reader commands do not create or
  depend on a local SQLite database.
- Keep the existing local-first and Git-backed modes unchanged.
- Use Cloudflare D1 as the hosted SQLite substrate so app schemas remain close
  to the current SQLite schemas.
- Use GitHub org/team auth for humans and agents, following the Crabbox
  browser-login plus bearer-session pattern.
- Let publishers update remote archives without forcing readers to have
  GitHub, Discord, Cloudflare, or bot credentials.
- Preserve `discrawl` privacy boundaries: local wiretap DMs and `@me` data stay
  out of shared remote archives by default.
- Leave room for later Worker-native sync, R2-backed content-addressed shards,
  and distributed cache federation.

## Non-goals

- Do not replace local SQLite.
- Do not replace Git-backed portable stores.
- Do not put GitHub or Discord provider clients in `crawlkit`.
- Do not expose raw SQL to ordinary remote readers.
- Do not make `crawlkit` know what a GitHub PR, Discord DM, guild, channel, or
  maintainer report means.
- Do not implement the future distributed cache layer in the first version.

## Current state

`crawlkit` owns reusable archive mechanics:

- config paths
- SQLite open/query helpers
- snapshot packing/import
- Git mirror mechanics
- sync state helpers
- control/status metadata
- TUI primitives

`gitcrawl` is local SQLite first. It has a Git-backed portable store where a
published SQLite database is cloned, copied into a runtime mirror for local
writes, and used by read commands. It also has a `gh` shim that serves many
read-only GitHub queries from local SQLite and falls back or hydrates as needed.

`discrawl` is also local SQLite first. It can publish a private Git snapshot
through `crawlkit/snapshot` and `crawlkit/mirror`, then read commands can
auto-import a stale share before querying. `discrawl` deliberately excludes
wiretap `@me` data from Git snapshots and preserves local `@me` data during
imports.

`crabbox` already has the auth shape we want:

- CLI starts a GitHub OAuth flow against a Cloudflare Worker.
- Worker requires GitHub `read:user`, `user:email`, and `read:org`.
- Worker checks allowed orgs and optional teams.
- Worker issues a signed bearer token.
- CLI stores the broker token in user config.
- Worker optionally verifies Cloudflare Access JWTs and derives an identity.

`openclaw/maintainers` shows the operational shape this can later support:

- org-wide live GitHub reads
- daily reports
- claim logs for multi-agent work splitting
- durable generated artifacts

The remote archive should make those workflows query live hosted archives
instead of cloning state repos or keeping each agent's local DB warm.

## Modes

Every app should have an explicit archive mode.

| mode | storage | intended use |
| --- | --- | --- |
| `local` | local SQLite | existing default; full sync/search/TUI/write behavior |
| `git` | Git checkout plus local runtime mirror | existing portable-store/share behavior |
| `cloud` | Worker + D1 only | no local DB; remote reads and remote TUI |
| `hybrid` | local SQLite plus cloud import/write-through | later; local speed with shared live cache |
| `publisher` | local or ephemeral DB plus cloud ingest | scheduled or maintainer-controlled publishing |

The first implementation should ship `cloud` read mode and `publisher` ingest.
`hybrid` is useful later, but adding it first would muddy the compatibility
story.

## Archive naming

Use stable archive slugs that do not leak local paths or maintainer machine
details.

Suggested production slugs:

```text
gitcrawl/openclaw__openclaw
gitcrawl/openclaw__gitcrawl
discrawl/openclaw-maintainers
discrawl/openclaw-community
```

Suggested D1 database names:

```text
crawl-gitcrawl-openclaw-openclaw-prod
crawl-discrawl-openclaw-maintainers-prod
crawl-remote-meta-prod
```

The public archive slug is part of the API. The D1 binding name is internal to
the Worker and can change during migrations.

## Architecture

```text
gitcrawl/discrawl CLI
        |
        | crawlkit/remote HTTP client
        v
OpenClaw crawl Worker
        |
        | GitHub OAuth session, org/team policy, query allowlist
        v
Cloudflare D1 bindings
        |
        +-- gitcrawl archive DBs
        +-- discrawl archive DBs
        +-- remote metadata DB
```

The Worker should be a separate service, likely `openclaw/crawlcloud` or
`openclaw/crawl-remote`, rather than hidden inside either app. It will need
TypeScript, Wrangler, D1 migrations, Vitest tests, and deployment config. The
Go apps consume it through `crawlkit`.

## Why not direct D1 API from the CLI

Direct D1 API access is the wrong user abstraction.

- It requires Cloudflare credentials on every reader machine.
- It pushes authorization policy into each CLI.
- It makes raw SQL tempting.
- It cannot reuse the GitHub org/team auth pattern cleanly.
- It exposes too much blast radius for ordinary read-only archive users.

Direct D1 API usage is still useful for admin bootstrap, emergency export,
and migration jobs. Normal CLI traffic should go through the Worker.

## Cloudflare design constraints

D1 is close enough to SQLite that existing schemas can be carried forward, but
the service should design around remote execution instead of pretending D1 is a
local `database/sql` handle.

Important constraints:

- Query through a Worker D1 binding for normal app traffic.
- Use the Cloudflare D1 REST API only for admin/bootstrap tasks.
- Use prepared statements and bound parameters.
- Keep requests paginated.
- Avoid giant SQL statements and oversized bound rows.
- Treat batch ingest as chunked transactional units, not one enormous import.
- Verify FTS5, JSON, triggers, and PRAGMA behavior against Wrangler/D1 before
  claiming support for an app command.

The user-facing CLI should talk to the Worker. The Worker should talk to D1.

## Auth model

Use the Crabbox pattern, generalized:

```text
cli login
  -> POST /v1/auth/github/start
  -> browser GitHub OAuth
  -> GET /v1/auth/github/callback
  -> POST /v1/auth/github/poll
  -> signed bearer token stored locally
```

For non-browser bootstrap and CI/operator lanes, the CLI can also exchange an
existing GitHub token:

```text
cli login --github-token-env GITHUB_TOKEN
  -> POST /v1/auth/github/token
  -> Worker verifies GitHub user/org/team membership
  -> signed bearer token stored locally
```

The Worker must not store the supplied GitHub token. It only uses the token to
query GitHub, verifies the same org/team policy as OAuth, and returns an
OpenClaw remote session token.

Required GitHub OAuth scopes:

- `read:user`
- `user:email`
- `read:org`

Worker env:

```text
CRAWL_REMOTE_PUBLIC_URL
CRAWL_REMOTE_SESSION_SECRET
CRAWL_REMOTE_GITHUB_CLIENT_ID
CRAWL_REMOTE_GITHUB_CLIENT_SECRET
CRAWL_REMOTE_DEFAULT_ORG=openclaw
CRAWL_REMOTE_GITHUB_ALLOWED_ORGS=openclaw
CRAWL_REMOTE_GITHUB_ALLOWED_TEAMS=openclaw/maintainer
CRAWL_REMOTE_ADMIN_TOKEN
CRAWL_REMOTE_ACCESS_TEAM_DOMAIN
CRAWL_REMOTE_ACCESS_AUD
```

Roles:

| role | source | capabilities |
| --- | --- | --- |
| `reader` | allowed GitHub org/team member | list archives, status, search, read TUI pages, named read queries |
| `publisher` | allowed publisher team or admin token | ingest batches, update metadata, run integrity checks |
| `admin` | admin token or admin team | migrations, archive registry, dangerous maintenance endpoints |

The bearer token should encode owner email, GitHub login, org, roles, issued
time, and expiry. Keep expiry finite, default 30 days. Do not store GitHub
access tokens after login unless a future Worker-native GitHub syncer truly
needs them. If that future syncer lands, store refreshable provider auth in a
separate encrypted Worker secret flow, not in reader tokens.

Cloudflare Access can sit in front of the Worker as defense in depth. The
Worker should verify `cf-access-jwt-assertion` when configured, then strip
Access headers before forwarding any request context.

## Config shape

Add provider-neutral remote config structs in `crawlkit`, then embed them in
each app config.

```toml
[remote]
mode = "local" # local | git | cloud | hybrid | publisher
endpoint = "https://crawl.openclaw.ai"
archive = "openclaw/gitcrawl/openclaw__openclaw"
token_env = "CRAWL_REMOTE_TOKEN"
stale_after = "2m"

[remote.auth]
token_source = "keyring" # keyring | env | config | none
keyring_service = "crawl-remote"
keyring_account = "openclaw"
```

For `gitcrawl`, keep existing portable-store config and add remote next to it.

```toml
[github]
token_env = "GITHUB_TOKEN"

[portable_store]
url = "https://github.com/openclaw/gitcrawl-store.git"
database = "data/openclaw__openclaw.sync.db"
checkout_dir = "~/.config/gitcrawl/portable"

[remote]
mode = "cloud"
endpoint = "https://crawl.openclaw.ai"
archive = "gitcrawl/openclaw__openclaw"
```

For `discrawl`, keep `[share]` as the Git-backed path and add `[remote]`.
`discord.token_source = "none"` remains valid for read-only cloud readers.

```toml
[discord]
token_source = "none"

[share]
remote = "https://github.com/openclaw/discord-store.git"
repo_path = "~/.discrawl/share"
auto_update = true

[remote]
mode = "cloud"
endpoint = "https://crawl.openclaw.ai"
archive = "discrawl/openclaw-maintainers"
```

In `cloud` mode, no app should require `db_path` to exist. Existing config
defaults can remain, but opening a local DB should be skipped for remote-only
commands.

## crawlkit package plan

Add a new `remote` package. Keep it transport-level and provider-neutral.

Proposed types:

```go
package remote

type Config struct {
    Mode       string
    Endpoint   string
    Archive    string
    TokenEnv   string
    StaleAfter string
    Auth       AuthConfig
}

type AuthConfig struct {
    TokenSource     string
    KeyringService  string
    KeyringAccount  string
}

type Client struct { /* HTTP client, endpoint, token provider */ }

type QueryRequest struct {
    App     string         `json:"app"`
    Archive string        `json:"archive"`
    Name    string        `json:"name"`
    Args    map[string]any `json:"args,omitempty"`
    Limit   int           `json:"limit,omitempty"`
    Cursor  string        `json:"cursor,omitempty"`
}

type QueryResult struct {
    Columns    []string         `json:"columns"`
    Rows       [][]any          `json:"rows"`
    Values     []map[string]any `json:"values,omitempty"`
    Cursor     string           `json:"cursor,omitempty"`
    Stats      QueryStats       `json:"stats,omitempty"`
    SchemaHash string           `json:"schema_hash,omitempty"`
}

type Status struct {
    App          string
    Archive      string
    Mode         string
    GeneratedAt  string
    LastSyncAt   string
    LastIngestAt string
    Counts       []control.Count
    Capabilities []string
    Warnings     []string
}
```

Package responsibilities:

- normalize endpoint URLs
- attach bearer auth
- run login start/poll helper methods
- expose `Whoami`, `Status`, `Query`, `BatchRead`, and `Ingest` primitives
- map remote errors to typed Go errors
- provide test servers for app unit tests
- add control/status JSON helpers for remote archives

Do not add app-specific query names, schemas, rows, or auth policies here.

Update `control` to represent non-file databases:

```go
type Database struct {
    ID         string
    Label      string
    Kind       string // sqlite | d1 | remote
    Role       string
    Path       string // local path when kind=sqlite
    Endpoint   string // remote endpoint when kind=d1|remote
    Archive    string
    IsPrimary  bool
    Bytes      int64
    Counts     []Count
}
```

Existing JSON fields should stay additive. No current consumer should break.

## Worker service plan

Create a Cloudflare Worker service with:

- D1 bindings for app archives
- a metadata binding for archive registry and auth audit
- GitHub OAuth routes copied from Crabbox and renamed
- optional Cloudflare Access verification copied from Crabbox
- query allowlists per app
- ingest allowlists per app
- Vitest coverage for auth, policy, query routing, and ingest validation
- Wrangler local D1 tests

Suggested routes:

```text
POST /v1/auth/github/start
GET  /v1/auth/github/callback
POST /v1/auth/github/poll
GET  /v1/whoami

GET  /v1/archives
GET  /v1/apps/:app/archives/:archive/status
POST /v1/apps/:app/archives/:archive/query
POST /v1/apps/:app/archives/:archive/batch-read
POST /v1/apps/:app/archives/:archive/ingest
POST /v1/apps/:app/archives/:archive/integrity
```

Do not add a general `/sql` route for readers. For admin-only diagnostics, make
raw SQL opt-in and disabled in production unless a break-glass env var is set.

Deployment environments:

| env | purpose | auth |
| --- | --- | --- |
| `local` | Wrangler/Vitest fixtures | fake GitHub/OAuth and local D1 |
| `staging` | real GitHub org auth, disposable D1 data | restricted maintainer team |
| `prod` | live archives | org/team policy plus optional Access |

Archive registry table:

```sql
create table if not exists remote_archives (
  id text primary key,
  app text not null,
  slug text not null,
  d1_binding text not null,
  schema_name text not null,
  schema_version integer not null,
  schema_hash text not null,
  visibility text not null default 'org',
  read_role text not null default 'reader',
  publish_role text not null default 'publisher',
  created_at text not null,
  updated_at text not null,
  unique(app, slug)
);

create table if not exists remote_ingest_runs (
  id text primary key,
  archive_id text not null references remote_archives(id),
  actor_login text not null,
  actor_owner text not null,
  source text not null,
  status text not null,
  started_at text not null,
  finished_at text,
  stats_json text not null default '{}',
  error_text text
);
```

Each app archive D1 database should also have local metadata:

```sql
create table if not exists crawl_remote_metadata (
  key text primary key,
  value text not null,
  updated_at text not null
);
```

Required metadata keys:

- `app`
- `archive`
- `schema_name`
- `schema_version`
- `schema_hash`
- `last_ingest_at`
- `last_source_sync_at`
- `privacy_policy`
- `capabilities`

## Query policy

Remote reads should use named queries, not raw SQL strings from users.

Example request:

```json
{
  "name": "gitcrawl.search_threads",
  "args": {
    "repo": "openclaw/openclaw",
    "query": "provider auth",
    "state": "open"
  },
  "limit": 50
}
```

The Worker maps this to a prepared statement:

```sql
select number, kind, state, title, html_url, updated_at_gh
from threads
join repositories on repositories.id = threads.repo_id
where repositories.full_name = ?
  and state = ?
  and documents_fts match ?
order by updated_at_gh desc
limit ?
```

Benefits:

- stable API contract
- less SQL injection surface
- app-specific privacy filters remain enforceable
- D1 query cost stays visible per query name
- TUI pagination can be tuned by endpoint

## gitcrawl remote scope

First commands to support in `cloud` mode:

- `login`
- `whoami`
- `status --json`
- `metadata --json`
- `threads`
- `search issues|prs`
- `gh issue list`
- `gh issue view`
- `gh pr list`
- `gh pr view`
- `gh pr checks`
- `gh pr status`
- `tui`

Commands that should stay local or explicit in v1:

- `sync`: local/provider sync unless running as a publisher
- `refresh`, `embed`, `cluster`: local or publisher-only
- `close-thread`, `close-cluster`, overrides: local-only until conflict policy
  exists
- `portable prune`: Git-backed only

Remote query names:

```text
gitcrawl.status
gitcrawl.repositories
gitcrawl.threads.list
gitcrawl.threads.search
gitcrawl.thread.detail
gitcrawl.thread.comments
gitcrawl.pr.detail
gitcrawl.pr.files
gitcrawl.pr.commits
gitcrawl.pr.checks
gitcrawl.pr.review_threads
gitcrawl.workflow_runs
gitcrawl.clusters.list
gitcrawl.cluster.detail
gitcrawl.neighbors
gitcrawl.tui.initial
gitcrawl.tui.page
```

`gh` shim behavior in cloud mode:

- exact read supported if the remote archive has the row
- broad list/search supported through named queries
- auto-hydration must not silently write to remote for a normal reader
- if data is stale or missing, either fall back to real `gh` live read or return
  a structured cache miss depending on the existing shim policy
- publisher mode may expose `gitcrawl cloud hydrate --numbers ...` later

## discrawl remote scope

First commands to support in `cloud` mode:

- `login`
- `whoami`
- `status --json`
- `metadata --json`
- `search`
- `messages`
- `channels`
- `members`
- `mentions`
- `report`
- `digest`
- `tui`

Commands that should stay local or explicit in v1:

- `sync`: local bot or desktop cache sync unless publisher mode
- `tail`: local bot/Gateway only
- `wiretap`: local-only
- `dms`: local-only by default
- `publish`, `subscribe`, `update`: Git-backed share commands, unchanged
- `sql`: local-only for normal users; admin-only remote diagnostics later

Remote query names:

```text
discrawl.status
discrawl.guilds.list
discrawl.channels.list
discrawl.messages.search
discrawl.messages.page
discrawl.messages.by_channel
discrawl.members.search
discrawl.mentions.search
discrawl.report.summary
discrawl.digest.window
discrawl.tui.initial
discrawl.tui.page
```

Privacy invariant:

Every remote ingest and remote query must exclude `guild_id = '@me'` unless an
explicit future private-per-user archive is designed. The first shared remote
archive is org/guild shared data, not personal Discord Desktop data.

## Ingest model

V1 should use explicit publisher ingest, not Worker-native provider crawling.

`gitcrawl` publisher:

```bash
gitcrawl sync openclaw/openclaw --state open --include-comments --with pr-details
gitcrawl cloud publish --archive gitcrawl/openclaw__openclaw --remote https://crawl.openclaw.ai
```

`discrawl` publisher:

```bash
discrawl sync --full --skip-members=false
discrawl cloud publish --archive discrawl/openclaw-maintainers --remote https://crawl.openclaw.ai
```

Publisher implementation options:

1. Row-batch ingest from the live local DB.
2. Snapshot JSONL/Gzip export streamed to the Worker.
3. SQL export imported by admin tooling.

Recommendation: start with row-batch ingest for app tables plus a manifest.
It is easier to validate, easier to retry, and lets the Worker enforce table
allowlists and privacy filters. Keep the snapshot export path reusable for
bulk bootstrap, but do not make ordinary publish depend on Git.

Ingest request:

```json
{
  "manifest": {
    "app": "discrawl",
    "archive": "discrawl/openclaw-maintainers",
    "schema_version": 2,
    "schema_hash": "sha256:...",
    "mode": "replace",
    "source_sync_at": "2026-05-27T12:00:00Z"
  },
  "table": "messages",
  "columns": ["id", "guild_id", "channel_id", "author_id", "content", "created_at"],
  "rows": [["...", "..."]]
}
```

Replace ingest strategy:

1. Publisher starts ingest run.
2. Worker writes into shadow tables or uses archive generation suffixes.
3. Worker validates counts and `pragma quick_check` where supported.
4. Worker swaps metadata pointer or finalizes the replace.
5. Worker records `last_ingest_at` and actor.

For large archives, use append/update by natural primary keys after the replace
path is proven.

## Migration from state repos

Existing state repos should remain valid indefinitely. The cloud service starts
as another publish target.

Stage 1:

- Keep `openclaw/gitcrawl-store` and Discord store repos as canonical for
  existing readers.
- Add a scheduled publisher that reads the same source DB/snapshot and also
  publishes to D1.
- Compare table counts, freshness, and representative query output.

Stage 2:

- Make cloud mode the recommended reader path for OpenClaw maintainers.
- Keep Git stores as backup/export artifacts.
- Update docs to present Git stores as offline/portable fallback.

Stage 3:

- Decide whether the Git state repos become generated exports from D1, or
  whether they stop updating after a deprecation window.
- Do not remove Git support from apps; just stop making Git the only hosted
  sharing mechanism.

This keeps existing users safe while letting OpenClaw move its own shared
archives to live hosted reads.

## Schema strategy

Keep app schemas app-owned.

`gitcrawl` should publish a D1-compatible variant of its existing schema. The
remote archive can keep the same core tables, portable metadata, PR detail
tables, and FTS tables. Generated local-only or expensive tables can be omitted
unless a remote command needs them.

`discrawl` should publish the existing guild/channel/member/message schema,
`sync_state`, FTS tables, and optional embedding tables only after semantic
remote search is proven. `@me` rows remain excluded.

Add per-app schema files:

```text
gitcrawl/internal/remote/schema_d1.sql
discrawl/internal/remote/schema_d1.sql
```

Add schema hash generation:

```text
schema_hash = sha256(normalized schema_d1.sql + query registry version)
```

The Worker refuses ingest when the client schema hash is unknown or newer than
the deployed query registry.

## Local code refactor shape

Do not try to make `database/sql` magically remote. That will turn gross fast.

Instead, split app store usage into reader interfaces and writer stores.

For `gitcrawl`:

```go
type ThreadReader interface {
    SearchThreads(ctx context.Context, opts ThreadSearchOptions) ([]Thread, error)
    ThreadDetail(ctx context.Context, repo string, number int) (ThreadDetail, error)
    PullRequestStatus(ctx context.Context, repo string, number int) (PRStatus, error)
    Status(ctx context.Context) (Status, error)
}

type LocalStore struct { /* existing */ }
type RemoteStore struct { client *remote.Client }
```

For `discrawl`:

```go
type ArchiveReader interface {
    Search(ctx context.Context, opts SearchOptions) ([]SearchResult, error)
    Messages(ctx context.Context, opts MessageOptions) ([]MessageRow, error)
    Members(ctx context.Context, opts MemberSearchOptions) ([]MemberRow, error)
    Status(ctx context.Context) (Status, error)
}
```

Read commands depend on the reader interface. Sync/publish commands still
depend on local writable stores.

## Command additions

Generic:

```bash
<app> login --remote https://crawl.openclaw.ai
<app> whoami
<app> remote status --json
<app> remote archives --json
```

`gitcrawl`:

```bash
gitcrawl init --remote https://crawl.openclaw.ai --archive gitcrawl/openclaw__openclaw
gitcrawl cloud publish --archive gitcrawl/openclaw__openclaw
gitcrawl cloud integrity --archive gitcrawl/openclaw__openclaw
```

`discrawl`:

```bash
discrawl subscribe-cloud https://crawl.openclaw.ai/discrawl/openclaw-maintainers
discrawl cloud publish --archive discrawl/openclaw-maintainers
discrawl cloud integrity --archive discrawl/openclaw-maintainers
```

Use `subscribe-cloud` instead of overloading `subscribe`; existing `subscribe`
means Git snapshot today.

## Status and metadata

Remote status should make the storage mode obvious:

```json
{
  "schema_version": "crawlkit.control.v1",
  "app_id": "discrawl",
  "state": "ok",
  "summary": "124122 messages across 88 channels",
  "database_path": "",
  "databases": [
    {
      "id": "primary",
      "label": "Discord archive",
      "kind": "d1",
      "role": "archive",
      "endpoint": "https://crawl.openclaw.ai",
      "archive": "discrawl/openclaw-maintainers",
      "is_primary": true
    }
  ],
  "share": {
    "enabled": false
  },
  "remote": {
    "enabled": true,
    "mode": "cloud",
    "endpoint": "https://crawl.openclaw.ai",
    "archive": "discrawl/openclaw-maintainers",
    "last_ingest_at": "2026-05-27T12:00:00Z"
  }
}
```

Additive JSON only. Existing status consumers should ignore new fields.

## Backup and recovery

Use three layers:

1. D1 native export for disaster recovery.
2. App-level row-batch manifest exports for reproducible app restores.
3. Existing Git-backed portable stores as a compatibility backup until the
   cloud path has proven itself.

Operational commands:

```bash
wrangler d1 export crawl-gitcrawl-openclaw-openclaw-prod --remote --output backup.sql
gitcrawl cloud export --archive gitcrawl/openclaw__openclaw --output backup.jsonl.gz
discrawl cloud export --archive discrawl/openclaw-maintainers --output backup.jsonl.gz
```

The app-level export should preserve the same privacy policy as ingest. A
`discrawl` cloud export must not include `@me` rows.

## Rollout plan

### Phase 0: spec and compatibility inventory

- Land this spec.
- Add issue checklists for `crawlkit`, `gitcrawl`, `discrawl`, and the Worker.
- Capture current command matrices for local and Git modes.

Exit criteria:

- No behavior change.
- Agreement that Worker + D1 is the first target.

### Phase 1: crawlkit remote primitives

- Add `remote` package.
- Add token-source abstraction with env and config sources first.
- Add keyring source only if apps already have a dependency or it can stay
  optional.
- Add `control` remote database/status fields.
- Add httptest fixtures for query, status, auth, errors, pagination, and ingest.

Exit criteria:

- `GOWORK=off go test -count=1 ./...`
- No downstream app needs to change unless it opts into `remote`.

### Phase 2: Worker skeleton

- Create Worker repo.
- Port Crabbox GitHub OAuth/session code under neutral names.
- Add GitHub org/team policy.
- Add D1 metadata binding.
- Add `/whoami`, `/archives`, `/status`, and one fake query.
- Add Wrangler local D1 tests.

Exit criteria:

- `wrangler dev` local login/query works.
- Vitest covers org deny, team deny, expired token, and reader/publisher roles.

### Phase 3: gitcrawl read-only cloud mode

- Add `[remote]` config.
- Add remote client wiring.
- Extract read interfaces for search/thread/detail/status.
- Implement named Worker queries for core read commands.
- Wire cloud mode into `gh` shim for read-only supported shapes.
- Ensure no local DB is created in cloud mode for supported commands.

Exit criteria:

- Existing local and portable-store tests still pass.
- New cloud-mode tests prove no local SQLite file is created.
- Remote search/detail output matches seeded local fixtures.

### Phase 4: discrawl read-only cloud mode

- Add `[remote]` config.
- Add `subscribe-cloud`.
- Extract read interfaces for search/messages/members/report/status/TUI.
- Implement remote named queries.
- Enforce `@me` exclusion in ingest and query fixtures.
- Keep existing Git `publish/subscribe/update` untouched.

Exit criteria:

- Existing Git share tests still pass.
- New cloud-mode tests prove no local SQLite file is created.
- Remote reads never return `@me` rows.

### Phase 5: publisher ingest

- Add `gitcrawl cloud publish`.
- Add `discrawl cloud publish`.
- Add Worker ingest endpoints with publisher role checks.
- Start with replace-mode ingest into D1.
- Add integrity endpoint comparing table counts, schema hash, and metadata.

Exit criteria:

- A temp local DB can publish to local Wrangler D1.
- Reader commands can query the published D1 with no local DB.
- Existing Git-based publishing remains unchanged.

### Phase 6: OpenClaw hosted pilot

- Deploy staging Worker.
- Create staging D1 archives:
  - `gitcrawl/openclaw__openclaw`
  - `discrawl/openclaw-maintainers`
- Seed from current local or CI publishers.
- Add one maintainer daily report read path against remote.
- Compare remote report output against local/Git store output.

Exit criteria:

- Staging readers can login with GitHub org auth.
- No local DB is created for supported read commands.
- Report/search/TUI latency and D1 read counts are acceptable.

## Validation matrix

`crawlkit`:

```bash
GOWORK=off go mod tidy
git diff --exit-code -- go.mod go.sum
GOWORK=off go vet ./...
GOWORK=off go test -count=1 ./...
```

`gitcrawl`:

```bash
GOWORK=off go test ./...
gitcrawl --config "$TMP/config.toml" init --remote "$REMOTE" --archive gitcrawl/openclaw__openclaw --json
gitcrawl --config "$TMP/config.toml" status --json
gitcrawl --config "$TMP/config.toml" search issues "auth" -R openclaw/openclaw --json number,title,url
test ! -e "$TMP/gitcrawl.db"
```

`discrawl`:

```bash
GOWORK=off go test ./...
discrawl --config "$TMP/config.toml" subscribe-cloud "$REMOTE/discrawl/openclaw-maintainers" --json
discrawl --config "$TMP/config.toml" status --json
discrawl --config "$TMP/config.toml" search "github" --json
discrawl --config "$TMP/config.toml" messages --channel maintainers --last 20 --json
test ! -e "$TMP/discrawl.db"
```

Worker:

```bash
npm test
npx wrangler d1 migrations apply crawl-remote-meta --local
npx wrangler d1 migrations apply gitcrawl-openclaw --local
npx wrangler d1 migrations apply discrawl-maintainers --local
npx wrangler dev
```

Security checks:

- reader cannot ingest
- reader cannot raw SQL
- publisher cannot admin migrate
- expired token fails
- wrong org fails
- wrong team fails when teams are configured
- Access JWT is verified when configured
- `discrawl` remote ingest rejects `@me`
- token values are redacted in logs and JSON status

Compatibility checks:

- existing `gitcrawl` local commands pass
- existing `gitcrawl` portable-store tests pass
- existing `discrawl` Git share tests pass
- existing `discrawl` wiretap local tests pass
- existing `crawlkit` snapshot/mirror/store tests pass

## Risks

### Remote SQL is not local SQLite

D1 supports SQLite semantics, but not every local pragma, import shape, or
runtime assumption is equivalent. Do not reuse local SQL blindly. Keep a D1
schema file and query registry tested under Wrangler.

### Arbitrary SQL is a foot-gun

`discrawl sql` is useful locally, but exposing it remotely would turn the
archive into an exfiltration API. Keep remote reads named and paginated.

### `gh` shim auto-hydration can mutate by surprise

In local mode, auto-hydration writing to a local runtime DB is fine. In cloud
mode, a reader should not silently write to shared D1. Make mutation explicit
and role-gated.

### D1 database size

Use separate D1 databases by app/archive and shard before the archive is close
to D1 limits. Good boundaries are repo for `gitcrawl` and guild/community for
`discrawl`.

### Discord DMs

The fastest way to break trust is to accidentally upload wiretap DMs. Treat
`@me` exclusion as a hard test fixture in every ingest path.

### Worker-native provider sync is tempting

Do not start there. `gitcrawl` and `discrawl` already have local syncers. Use
publisher ingest first, then decide if a Worker-native GitHub syncer is worth
it. Discord Gateway/live tail is a worse fit for a first Worker service.

## Future direction

The later distributed cache idea should be modeled as provenance-aware shared
cache, not anonymous row gossip.

Possible future shape:

- D1 stores canonical indexes and metadata.
- R2 stores content-addressed snapshot shards.
- Clients can submit signed cache shards.
- Worker verifies schema hash, source provenance, freshness, and privacy
  filters.
- Trusted shards are merged into per-archive D1 indexes.
- Untrusted shards remain quarantined or private.

This gives the IPFS-like property that machines can contribute cache work, but
keeps OpenClaw policy centralized enough to avoid poisoning, privacy leaks, and
schema drift.

The strategic move is to make `crawlkit` own the transport and archive
contracts now, while leaving app semantics in the apps. That keeps today's Git
syncs stable and gives OpenClaw a clean path from "state repos with files" to
"live hosted archives with optional distributed cache."

## Implementation checkpoint - 2026-05-27

The current implementation pass now covers the shared client, both app
publisher/read paths, and a standalone Cloudflare Worker/D1 service prototype.
The design still keeps Cloudflare auth, D1 schema migration, Worker routing,
and app-specific table policy out of `crawlkit`.

Implemented branches:

- `openclaw/crawlkit` branch `spec/cloudflare-remote`
  - Added provider-neutral `remote` package.
  - Added GitHub login poll-secret helpers for Worker OAuth flows.
  - Added GitHub-token login exchange helpers for non-browser org/team auth.
  - Added `control.Remote` and remote database inventory fields.
  - Added this spec and synced the living copy to `~/.spec`.
- `openclaw/gitcrawl` branch `feature/cloudflare-remote-archives`
  - Added `[remote]` config, env overrides, remote token resolution, `init --remote`,
    GitHub-backed `remote login` with OAuth or `--github-token-env`,
    `remote status`, `remote archives`, `whoami`, cloud-mode `status`,
    owner/repo search, and gh-shaped `search issues|prs` routing.
  - `remote login` stores the Worker-issued bearer token in the OS keyring.
  - Added `gitcrawl cloud publish` to push local repository/thread rows to the
    Worker ingest endpoint.
  - Cloud mode rejects local runtime opens for local-only commands rather than
    creating or mutating SQLite.
  - `doctor` reports remote endpoint/archive/token/status health without opening
    local SQLite.
- `openclaw/discrawl` branch `feature/cloudflare-remote-archives`
  - Added `[remote]` config, `subscribe-cloud`, `remote status`,
    GitHub-backed `remote login` with OAuth or `--github-token-env`,
    `remote archives`, `remote whoami`, top-level `whoami`, cloud-mode
    `status`, cloud-mode `search`, and filtered cloud-mode `messages`.
  - `remote login` stores the Worker-issued bearer token in the OS keyring.
  - Added `discrawl cloud publish` to push non-DM guild/channel/member/message
    rows to the Worker ingest endpoint.
  - Existing Git `publish` / `subscribe` / `update` share commands remain the
    Git-backed path.
  - Cloud-mode status maps Worker archive status into crawlkit control status
    without opening the local store.
- `openclaw/crawl-remote` Worker repo
  - Added Wrangler/D1 service scaffold, migration, typed route handlers, and
    Vitest coverage.
  - Added GitHub OAuth start/callback/poll flow, GitHub-token bootstrap,
    allowed org/team checks, signed bearer sessions, and admin-token bootstrap
    auth.
  - Added role-gated archive status/list/query/batch-read/ingest routes.
  - Added named D1 queries for `gitcrawl.threads.search`,
    `discrawl.messages.search`, and `discrawl.messages.list`.
  - Added ingest table allowlists for gitcrawl and discrawl, including
    `@me`/DM row rejection for discrawl.
  - Created remote D1 database `crawl-remote`
    (`42baacd3-c917-400f-a12f-e0fada21e11f`) and deployed the Worker to
    `https://crawl-remote.services-91b.workers.dev`.

Validation completed:

- `crawlkit`: `GOWORK=off go mod tidy`, clean `go.mod`/`go.sum`,
  `GOWORK=off go vet ./...`, and `GOWORK=off go test -count=1 ./...`.
- `gitcrawl`: `GOWORK=off go test -count=1 ./...`, plus autoreview after
  remote gh-search and doctor fixes. Additional focused coverage verifies
  `gitcrawl cloud publish` ingest shape.
- `discrawl`: `GOWORK=off go test -count=1 ./...`, plus autoreview. Additional
  focused coverage verifies cloud status/search/messages without local SQLite
  and `discrawl cloud publish` non-DM ingest shape.
- Worker: `npm run typecheck`, `npm test`, and local Wrangler D1 migrations.
- Deployed Worker smoke: `/health`, `/v1/whoami`, and `/v1/archives` succeed
  against `https://crawl-remote.services-91b.workers.dev` with admin-token
  auth.
- Deployed GitHub org/team auth:
  - `POST /v1/auth/github/token` verifies the current GitHub identity as
    `openclaw` org plus `openclaw/maintainer` team, returns a Worker session
    token, and that token succeeds against `/v1/whoami`.
  - `gitcrawl remote login --github-token-env GH_TOKEN` stores the returned
    remote session token in the OS keyring, `gitcrawl whoami` reads it back
    from the deployed Worker, and the temp config run creates no SQLite DB.
  - `discrawl remote login --github-token-env GH_TOKEN` stores the returned
    remote session token in the OS keyring, `discrawl whoami` reads it back
    from the deployed Worker, and the temp config run creates no SQLite DB.
- Deployed Worker/D1 end-to-end:
  - `gitcrawl cloud publish` pushed a temp SQLite archive to the deployed
    Worker, then cloud search read the row back from remote D1.
  - `discrawl cloud publish` pushed a temp non-DM SQLite archive to the
    deployed Worker, then a reader config with no SQLite database ran cloud
    `search` and `messages` against remote D1. The reader DB stayed absent.
- Local Worker/D1 end-to-end:
  - `gitcrawl cloud publish` pushed a temp SQLite archive to
    `http://127.0.0.1:8787`, then cloud search read the row back from D1.
  - `discrawl cloud publish` pushed a temp non-DM SQLite archive to
    `http://127.0.0.1:8787`, then a reader config with no SQLite database ran
    cloud `search` and `messages` against D1. The reader DB stayed absent.

Dependency state:

- `gitcrawl` and `discrawl` temporarily depend on crawlkit pseudo-version
  `v0.7.1-0.20260527174716-74916a98ee45`.
- Release should tag crawlkit first, then bump both apps from the pseudo-version
  to the released tag before final app releases.

Compression note:

- No gzip/binary snapshot compression was added to the read path. Keep
  compression in publisher ingest or R2 snapshot layers so D1 named queries over
  indexed rows and vector metadata stay normal SQL.

Remaining release work:

- Decide when to make `openclaw/crawl-remote` public; it was created private
  for the first infra scaffold push.
- Optional browser-login polish: configure the real GitHub OAuth app
  client id/secret for callback
  `https://crawl-remote.services-91b.workers.dev/v1/auth/github/callback`.
  The deployed remote path already has live GitHub org/team auth through
  `--github-token-env`.
- Tag `crawlkit`, then bump `gitcrawl` and `discrawl` from the pseudo-version
  to the release tag.
