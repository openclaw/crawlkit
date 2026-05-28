# Remote Contract

`crawlkit/remote` owns the provider-neutral client and v1 wire contract for
hosted crawl archives. It does not own the Cloudflare Worker deployment.

The current service implementation lives in `openclaw/crawl-remote`. That repo
owns Wrangler config, D1 migrations, GitHub org/team authorization, Worker
secrets, and deployment cadence.

## Ownership

| surface | owner |
| --- | --- |
| Go client, config, request/response structs | `crawlkit/remote` |
| protocol version and public contract metadata | `crawlkit/remote` |
| app CLI commands and privacy-aware publishing | downstream apps |
| Cloudflare Worker routes and auth implementation | `openclaw/crawl-remote` |
| D1 schema and migrations | `openclaw/crawl-remote` |
| Wrangler deploy config and Worker secrets | `openclaw/crawl-remote` |

The Worker exposes `GET /v1/contract` without authentication. Clients can use
that route to verify protocol support before login or before publishing.

## Compatibility

The v1 contract is additive:

- Existing JSON fields stay stable.
- New routes, query names, app capabilities, and optional fields may be added.
- Breaking route or field changes require a new protocol version.
- App-specific privacy rules stay in the app or Worker implementation, not in
  generic crawlkit SQL helpers.

`remote.BaseContract()` is the SDK-side source of truth for protocol routes and
auth role names. Service implementations add their own app-specific query names,
ingest tables, capabilities, and privacy notes to `/v1/contract`, then run
their own conformance tests.
