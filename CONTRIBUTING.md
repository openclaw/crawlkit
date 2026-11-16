# Contributing

## Development

```bash
make help
make check
```

Linux CI runs the same `make check` target, including formatting, analysis,
unit/race tests, and release-script tests. Automatic runs cancel superseded
runs for the same PR or branch; manual downstream qualification retains its
existing job-level serialization.

Keep public APIs small. `crawlkit` should own reusable local archive mechanics,
not provider-specific Slack, Discord, Notion, or GitHub behavior.

## Independent review

Before delivery, request independent P0–P3 review through an ordinary native
subagent in the active agent environment. Native Codex and OpenClaw subagents
are both supported. Use isolated reviewer context. Pin the base commit and the
complete candidate (commit/tree or an exact manifest covering staged, unstaged and
task-owned untracked changes). Supply the required unchanged context, scope,
validation evidence and known limitations. Keep the reviewer read-only and do
not modify the candidate while review is running.

Resolve findings before delivery. If revisions are needed, pin the updated
candidate and re-review the changed boundaries. Record the reviewed pin and
result with the handoff. Do not invoke a scripted review CLI, `codex exec`,
`agent exec`, wrapper or replacement helper. This review does not replace the
existing tests, CI, secret scanning or release verification, and it does not
authorize provider access, runtime changes or publication beyond the task scope.

## Compatibility

This module starts at `v0`, so APIs may still change. Prefer additive changes
and keep downstream crawler rewires narrow.
