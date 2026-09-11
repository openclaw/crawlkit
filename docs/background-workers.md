# Background workers

`worker` supplies continuous bounded execution, not another cron scheduler.
Use `scheduler`/`crawlctl` for periodic commands and `worker.Runner` within a
long-running app for content-triggered work. Separate runners isolate task kinds.

The generic `Queue[P, R]` adapter owns durable claims, completion, retries and
release. `Handler[P, R]` performs expensive work on the bounded background pool,
outside all database transactions. It must honor context cancellation. The
runtime catches handler panics but cannot forcibly terminate a Go goroutine that
ignores cancellation. Apps retain content selection, schema and privacy rules.

## Required storage contract

- Save source content and enqueue its job in the same transaction. Notify only
  after commit; notification is a coalesced hint and polling recovers lost hints.
- Give jobs stable keys and opaque input revisions. Increment a generation or use
  a content hash when meaningful input changes. A latest-value job can replace
  pending work; an event-processing app can use a distinct key for every event.
- Claim ready work atomically with a fresh random token and an expiry. Enforce
  the requested batch bound and priority class using indexed queries.
- Fence **every** completion, retry and release by key, revision, token, pending
  state and an unexpired lease. Recheck current source eligibility at completion.
  Persist the result and successful job state atomically. A stale lease returns
  false without mutating data, including after a source is deleted/recreated.
- A process crash leaves recoverable leases. External side effects require
  application idempotency keys; execution is at least once, not exactly once.
- Use independent read connections for costly preparation. Claims and result
  commits must be small and bounded so they do not monopolize ingestion's writer.

Discrawl can adapt message generations and embedding rows; Gitcrawl can adapt
thread content hashes and vectors without renaming its existing schema. The
SQLite test adapter exercises lease recovery, stale edits, deletion/recreation,
duplicate completion and the hash-revision shape. Independent task-kind tests
verify that a stalled handler does not block another runner.

## Defaults and operation

Two workers, up to 64 items per batch, a 250 ms collection window and one-second
fallback polling. Claims prefer fresh work four times then catch-up once, with
unused capacity borrowed. Each kind has its own resource bounds. The default
handler deadline is two minutes; storage calls have five-second deadlines.
Leases must cover the full handler plus bounded completion and failure-cleanup budget; explicit
shorter leases are rejected. No unbounded in-memory backlog is accumulated.

Failures carry safe error codes, never raw inputs or provider responses. Unknown
errors retry with exponential backoff and jitter (one second to one minute), up
to three attempts. `Failure.Pause` represents an unavailable dependency, pauses
that runner and preserves the attempt budget. `RetryAfter` is a minimum delay;
permanent errors mark jobs failed. Apps can resume failed work through their own
retry/rebuild contract. Queue failures are reported and retried while leases
remain recoverable. Cancellation releases unfinished claims when storage permits.

`Status` reports lifecycle, in-flight count, outcomes, retry time, last success and
a safe error code. Apps combine it with indexed pending counts and queue ages in
their status surface. Ten-second embedding freshness is a downstream acceptance
target under a measured workload and healthy provider, not a scheduling guarantee
during outages or sustained overload. Report lag rather than silently dropping work.
