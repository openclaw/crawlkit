# CI Helpers

`downstream-compat.sh` compares the complete Gitcrawl CLI test suite against a
pinned Crawlkit baseline and the candidate checkout. The source pins live in
`ci.yml`. Update pins deliberately, with exact-head review.

The automatic job deliberately advances its Crawlkit API baseline from
`26a574e53b485cd2191df603f02d17fab6d3c4af` to published v0.16.5,
`493f7470c5e366b52b228e49ed08807f6c81dbf4`. Current Gitcrawl requires
`IngestManifest.Warnings` and `ArchiveSnapshot.Warnings`, which the older baseline
does not provide. The job uses the published source directly, without API shims.

Its Gitcrawl pin advances from `dab4d849f9562306da41267cda2b2fd6868a41a8` to
`0a4cefe1858b5a8b8c1138260252b09589c4294f`. This is a
[34-commit refresh](https://github.com/openclaw/gitcrawl/compare/dab4d849f9562306da41267cda2b2fd6868a41a8...0a4cefe1858b5a8b8c1138260252b09589c4294f),
including CLI and storage refactors, archive admission, export and sync fixes,
GraphQL history, and dependency and release updates. It also includes the
fixture-only Git maintenance isolation in
[Gitcrawl PR215](https://github.com/openclaw/gitcrawl/pull/215).
This qualifies current downstream compatibility; it does not establish the
cause or repair of the historical Git 2.55 cleanup failure.

Both complete CLI suites remain required, against the published baseline and
the exact candidate head. Selected-app manual qualification retains baseline
`26a574e53b485cd2191df603f02d17fab6d3c4af` and its existing app pins.

The Linux job has a 20-minute ceiling. It prepares dependencies with Go 1.27.1
and the public Go proxy, then runs baseline and candidate serially with
`GOPROXY=off`. Each uses native `lsof`, private HOME/XDG/temp directories, a
private test modfile, and a network/PID namespace with loopback only. The
package test timeout remains three minutes; the outer six-minute ceiling also
covers compilation. There are no retries or provider collector commands.

Checkouts and source archives remain unchanged. Only private test inputs receive
the temporary Crawlkit replacement and native Go dependency normalization.
Source/module hashes, module resolution, native tool versions, complete test
output, exit codes, and log hashes are retained in the Actions log. Both phase
exit codes also appear in the job summary. A failing baseline is not a pass.

The namespace requires native `sudo`, `unshare`, `ip`, and `setpriv`; missing
capabilities fail the job. It does not alter the runner's host network. This
helper neither installs app binaries nor accesses live archives, credentials,
deployments, signing, tags, or releases.

Before dependency preparation, a cancellation probe uses the same privileged
`sudo timeout unshare` chain and UID/GID drop as the suites. `setpriv --pdeathsig
keep` restores the parent-death signal after the credential change. The probe
requires that signal to remain `SIGKILL`, creates a detached descendant, and
checks both owned processes exit when the eight-second timeout escalates after
one second. Private namespace/PID markers and pidfds bind cleanup to those exact
processes. Missing native capabilities, failed cancellation, or failed drainage
stop the job before Go phases; they are not compatibility passes.

## Selected App Qualification

The same `ci.yml` also accepts a manual `app` choice: `slacrawl`, `discrawl`, or
`notcrawl`. It checks out only that public app at its reviewed SHA, the pinned
Crawlkit baseline, and the exact dispatch head. No cross-repository credentials
are used. A manual run skips the ordinary library and Gitcrawl jobs; its single
Ubuntu job is serialized with other manual app runs and capped at 20 minutes.

The four-argument harness path prepares the complete app dependency graph, then
runs `go test -count=1 -timeout=3m ./...` and `go build ./cmd/<app>` against
baseline and candidate in order. The original three-argument Gitcrawl path
still selects only `./internal/cli`. Both use the same cancellation probe,
private module files, source integrity checks, native tools, and offline
namespace. Builds have a two-minute outer ceiling; test compilation and
execution retain the six-minute outer ceiling and unchanged package timeout.
No phase is retried.

`downstream-smoke.py` runs six commands against each successful build:
`--help`, `--version`, `tui --help`, `metadata --json`, `status --json`, and
`tui --json`. Each command has a 30-second ceiling; the complete smoke phase
has a two-minute outer ceiling. Smoke HOME/XDG/temp directories are separate
from the test runtime. Slacrawl uses native `init --db` and Notcrawl uses native
`init` to create local configuration. Discrawl uses its native absent-config
and absent-archive path: its authenticated `init` is never called.

Every successful smoke requires nonempty help/version output, valid JSON, and
unchanged runtime file contents, modes, and symlinks after fixture setup.
Baseline/candidate JSON must match after normalizing only private runtime path
prefixes and the top-level status `generated_at` clock. Commands, outputs,
binary build information, file digests, snapshots, and result JSON are printed
to the Actions log. A failed test, build, smoke, or comparison fails the job;
an unchanged baseline failure remains a limitation, not a pass.

For pre-merge qualification, dispatch this existing workflow by file name and
the reviewed PR branch ref. GitHub's
[event contract](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_dispatch)
allows API/CLI dispatch against a branch or tag once the workflow has run.
The file already exists on the default branch; this does not require merging
the candidate first. Check the exact branch head before every dispatch and
bind the returned run to that head. Finish one selected app before dispatching
the next; do not enqueue multiple runs or automatically retry failures.
