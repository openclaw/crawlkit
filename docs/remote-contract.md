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

## Credential Transport

Bearer requests, GitHub-token login and login polling require HTTPS, except
for explicitly configured loopback HTTP development endpoints. Credential-bearing
redirects must retain the original scheme, hostname and effective port, including
307/308 redirects that replay a login body. Caller redirect policies may further
restrict redirects; the client does not mutate the caller's HTTP client.

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

## Optional incremental change contract

`ChangeBatch`, `EncodeChangeBatch` and `Client.ApplyChanges` describe the optional
`changes-v1` contract at `POST /v1/apps/:app/archives/:archive/changes`.
These client APIs do not establish that a receiver exists. `BaseContract()` does
not advertise this route or capability. A caller must verify receiver support
before delivery; unsupported receivers never fall back to snapshot ingestion.
Existing snapshot manifests, ingest results and cutover behavior are unchanged.

The source owns capture, explicit privacy-aware projection, registration and its
durable SQLite outbox. A batch contains only the approved service projection.
Native file identities, paths, admission metadata, raw payloads and media/cache
paths have no implicit permission to leave the source. `BindingID` refers to an
immutable receiver registration containing the approved scope, profile and
qualified native baseline. It grants no authority; existing publisher
authorization still applies. The binding's profile defines table names, key and
row column order, allowed values and privacy rules. Crawlkit does not infer them.

`ChangeValue` has kind `null`, `text` or `integer`. Null omits `value`; text uses a
non-null string pointer, including for empty text. Integers use canonical signed
decimal strings in the int64 range, without a plus sign, leading zeros or `-0`.
UTF-8 including NUL, exact integer values and NULL versus empty text are preserved.
Receivers must use a lossless storage representation for NUL-bearing text.
An upsert supplies a full profile-defined row; a removal omits the row and carries
only its key. Removal records an observation that the key left the admitted
current set, not necessarily an upstream deletion. Retain earlier versions.
Absent rows alone do not establish removals, and changed scope needs reconciliation.

Request limits are 4 MiB of exact JSON bytes, 500 observations per part and 64
parts per capture. Part indices are zero-based. App/archive/binding/table metadata
is nonempty and at most 128 UTF-8 bytes. Native anchors are nonempty, opaque and
at most 512 UTF-8 bytes; the expected archive cursor has the same limit but may
be empty for an admitted initial delivery. Metadata cannot contain NUL. Capture
IDs are source-allocated 32-character lowercase hexadecimal identifiers, kept
stable across retries. The derived batch ID and next archive cursor are both
`chg1:<capture_id>:<part_index>`. An empty observation array can advance a native
interval containing only excluded changes. The source must separately bound its
capture/outbox resources; the protocol's maximum is not an RSS guarantee.

The outbox persists the complete ordered part set, exact request bytes and hashes,
expected/next archive cursors and final native watermark before sending any part.
`EncodeChangeBatch` is a deterministic convenience, not a canonical-JSON mandate.
`ApplyChanges` accepts valid field ordering and whitespace and sends the supplied
bytes unchanged. Do not mutate that byte slice until the call returns. Unknown,
duplicate, case-aliased, missing or explicitly null object fields are rejected;
NULL scalars use the representation above. Invalid UTF-8 or unpaired surrogate
escapes fail instead of being replaced. Payload identity is SHA-256 of the exact
persisted bytes, never a re-encoding or JSONB normalization. Different whitespace
under the same batch ID is different content.

A receiver must lock the source and commit row versions, key-only removals, batch
receipt and archive cursor together. Same-ID/same-hash replay returns its receipt;
changed content conflicts. A new part requires its exact expected archive cursor.
Part zero starts at the accepted complete native watermark; subsequent parts
follow the preceding receipt with the same capture, binding, native range and
part count, and consecutive indices. Only the final part records the complete
native watermark. This is per-part atomicity, not whole-capture visibility or a
claim of acquisition completeness. These are receiver requirements, not behavior
implemented by this Go client.

The client verifies receipt app/archive/binding, derived batch ID, raw payload
hash and next cursor. The service commit sequence must be a canonical decimal
string in `1..9223372036854775807`; it is delivery order, not provider event time.
After every part's matching receipt is durable, the source atomically acknowledges
the outbox and advances its native cursor. On an unknown HTTP outcome, retain and
replay the same persisted bytes; do not capture a replacement first. The client
does not automatically retry, allocate IDs, register bindings or activate workers.
