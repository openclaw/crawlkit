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

## Compatibility

This module starts at `v0`, so APIs may still change. Prefer additive changes
and keep downstream crawler rewires narrow.
