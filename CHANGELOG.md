# Changelog

## v0.7.1 - Unreleased

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
