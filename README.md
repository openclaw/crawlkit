# 🧱 crawlkit

Shared Go infrastructure for local-first crawler archives.

`crawlkit` is not a universal Slack, Discord, Notion, or GitHub crawler. It is
the reusable foundation beneath those tools: SQLite hygiene, TOML config
defaults, portable JSONL/Gzip packing, git-backed snapshot sharing, sync state,
CLI output helpers, control/status metadata, a shared terminal explorer, and
safe desktop-cache snapshot utilities.

## Install

```bash
go get github.com/openclaw/crawlkit@latest
```

Go packages are published by tagging this repository. There is no separate
package registry step. See `docs/publishing.md` for the release commands.
See `docs/boundary.md` for the crawlkit-versus-app ownership boundary.

## Packages

- `config`: standard TOML config paths, opt-in platform-native runtime dirs,
  migration-safe legacy path fallback, and token diagnostics.
- `store`: SQLite open/read-only/transaction/query helpers.
- `snapshot`: `manifest.json` plus JSONL/Gzip table snapshot export, file fingerprints, full import, and planned incremental shard import.
- `backup`: age-encrypted JSONL/Gzip shards, backup manifests, recipient/identity helpers, and shard restore verification.
- `mirror`: clone/init/pull/commit/push helpers for private snapshot repos.
- `state`: generic crawler cursor and freshness records.
- `embed`: reusable OpenAI-compatible, Ollama, and llama.cpp embedding providers plus local probe diagnostics.
- `vector`: float32 vector encoding, dimension validation, cosine scoring, top-k helpers, and reciprocal-rank fusion.
- `releasecheck`: GitHub release checks, 24-hour cache handling, scripted-output
  suppression, and stderr update notice formatting for crawl app CLIs.
- `output`: text/json/log output helpers.
- `control`: crawl app metadata, command manifests, status payloads, and
  database inventory for launchers and automation.
- `tui`: shared terminal archive explorer with gitcrawl-style responsive panes, entity/member/detail lanes, compact sortable headers, mouse selection, floating right-click actions, sorting/filtering, and local/remote source status.
- `cache`: safe read-only local cache snapshot helpers.

## Downstream apps

- `gitcrawl` and `discrawl` consume `crawlkit` on `main`.
- `slacrawl` and `notcrawl` consume `crawlkit` on their `feat/use-crawlkit`
  integration branches until those app rewires are merged.
- The apps keep provider schemas, auth, desktop/API parsing, privacy filters,
  and user-facing CLI contracts. `crawlkit` owns only the reusable mechanics.

## Safety

Library tests use temporary directories. They do not touch app runtime stores
such as `~/.config/gitcrawl`, `~/.slacrawl`, `~/.discrawl`, or `~/.notcrawl`.
