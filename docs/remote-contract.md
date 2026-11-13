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

`ChangeBatch`, `EncodeChangeBatch`, `DecodeChangeBatch` and `Client.ApplyChanges`
describe the optional `changes-v1` contract at
`POST /v1/apps/:app/archives/:archive/changes`.
These APIs do not establish that a receiver exists. `BaseContract()` does
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
`DecodeChangeBatch` gives receivers the same strict wire validation as the client.
It does not authorize a publisher, binding or profile, and it does not replace the
original bytes that receivers must hash.
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

## Optional private Slack seed contract

`SlackSeedManifest`, `SlackSeedChunk` and `SlackSeedComplete` describe
`slacrawl-seed-v1`. This shared wire contract connects Slacrawl's explicitly
projected private seed to a receiver; it does not parse native schemas, collect
Slack data, authorize scope or implement a receiver. The three optional POST
routes are:

| Request | Route | Derived batch ID and next cursor |
| --- | --- | --- |
| Manifest | `/v1/apps/slacrawl/archives/:archive/seeds` | `seed1:<seed_id>:begin` |
| Chunk | `/v1/apps/slacrawl/archives/:archive/seeds/:seed_id/chunks/:index` | `seed1:<seed_id>:<index>` |
| Completion | `/v1/apps/slacrawl/archives/:archive/seeds/:seed_id/complete` | `seed1:<seed_id>:complete` |

These routes are absent from `BaseContract()`. No receiver, outbox migration,
source registration, worker or baseline activation is supplied by this library.
Existing snapshot and `changes-v1` contracts are unchanged.

The manifest requires contract, app, archive, binding ID, seed ID, profile,
native schema, native-through anchor, scope and tables. App is `slacrawl`, profile
is `slacrawl-private-archive-v1`, and native schema is `9`. Scope contains a
workspace ID, a nonempty unique channel-ID array and an explicit
`include_workspace_users` boolean. These are consistency facts to compare with
operator-approved admission, never authorization. Source-local file identities,
paths, cache paths, credentials and private admission metadata do not belong in
these envelopes. Row projection and raw-JSON provenance remain downstream policy.

Allocate a random 32-character lowercase hexadecimal seed ID before constructing
chunks. The real native anchor is
`slacrawl-native-v1:<generation>:<sequence>:<entry>`, where generation and entry
are 32 lowercase hexadecimal characters and sequence is a canonical decimal
int64 in `0..9223372036854775807`. H0 is valid: do not invent a journal event or
an incremental native-from anchor to bootstrap an empty journal.

Tables appear exactly once in this order: `workspaces`, `channels`, `users`,
`messages`, `message_files`, `message_mentions`. Every table includes a canonical
nonnegative decimal row count, SHA-256 and ordered chunk descriptors with integer
`rows`, integer `bytes` and SHA-256. Empty tables require `rows: "0"`, `chunks: []`
and SHA-256 of empty bytes. Without workspace-user opt-in, `users` must be empty.
Flattening the table descriptors defines contiguous global chunk indices; no
second per-table chunk index is needed.

Chunks contain identity fields, index, expected cursor and typed `Change`
observations. Every chunk contains 1..500 upserts from one descriptor-selected
table. Keys and full rows retain the existing NULL/text/integer scalar contract,
order and multiplicity. Column layouts and key/row correspondence remain the
source projector and receiver's responsibility. In particular, NULL or duplicate
natural keys are retained as evidence; this codec does not establish that such a
seed can become an incremental baseline. A receiver must hold affected scopes
ineligible until that ambiguity is resolved, without dropping rows or inventing
public row IDs.

All three request kinds reuse `MaxChangeBytes` (4 MiB), and chunks reuse
`MaxChanges` (500 observations). There are at most `MaxSeedChunks` (8192) chunks
across all tables. Each descriptor has 1..500 rows and 1..4 MiB of bytes. Total row
counts must equal descriptor sums, checked after bounding the total descriptor
count so arithmetic cannot overflow. Archive/binding/workspace/channel IDs use
the existing nonempty, NUL-free, 128-byte UTF-8 metadata limit. All fields are
required and non-null, including empty arrays. Existing strict JSON rules apply
at every object level, including descriptors and scalar values. Producer capture,
RSS and durable staging budgets are separate and must be bounded by the caller.

Hash exact persisted request bytes, not canonical JSON or decoded values:

1. Construct and persist each chunk body; chunks contain no manifest hash.
2. A descriptor's SHA-256 hashes its exact chunk body, including whitespace.
3. A table SHA-256 hashes the concatenation of the ordered **binary 32-byte chunk
   digests**, not their hexadecimal text and not re-encoded rows.
4. Encode the manifest and hash its exact bytes. Completion includes that
   `manifest_sha256` and the expected final archive cursor.

This order has no hash cycle. Encoders produce deterministic sender bytes;
`EncodeSlackSeedManifest` sorts its own copy of channel IDs and preserves table,
chunk and row order. Decoders accept valid alternative field order, whitespace
and channel order. That accepted representation remains the payload identity.

`DecodeSlackSeedManifest` returns an immutable `SlackSeedPlan` with the saved
manifest hash and flattened descriptors. `Manifest()` returns caller-owned
metadata. Reuse the plan: `ValidateChunk` checks one chunk against one indexed
descriptor, without rescanning the manifest. `DecodeSlackSeedChunk` alone checks
only the envelope/scalars; it cannot establish descriptor consistency.
`ValidateComplete` checks the exact manifest hash and final cursor, while
`DecodeSlackSeedComplete` alone validates its envelope.

`Client.BeginSlackSeed`, `ApplySlackSeedChunk` and `CompleteSlackSeed` take that
plan and persisted request bytes. Begin checks the saved manifest hash; chunk
and completion use the plan's contextual validators before credentials or HTTP.
All reuse existing authenticated transport and `ChangeReceipt` validation
(`SeedReceipt` is an alias), preserve supplied bytes and return conflicts without
hidden retries. Do not mutate supplied bytes until a call returns.

A new manifest requires an explicitly admitted pristine source. The first chunk
expects the begin cursor; each later chunk expects its predecessor; completion
expects the last chunk cursor, or begin when all tables are empty. Exact receipt
replay must precede expected-cursor rejection and return the original service
sequence. Every successful receipt uses a positive canonical int64 service
sequence, even when the native anchor is H0. Earlier receipts identify the
intended binding without making it live.

The producer must durably stage all exact requests before sending. The receiver
must retain every row by source/service-sequence/ordinal, verify all ordered
receipts, counts and hashes, and atomically commit completion plus the qualified
binding. Only that matching completion receipt can initialize the producer's
saved native baseline. Crash recovery replays saved bytes. Neither a begin/chunk
receipt, row counts nor the final seed binding establishes acquisition freshness
or complete upstream coverage. These lifecycle requirements belong to the
future source outbox and receiver implementation, not this codec/client slice.
