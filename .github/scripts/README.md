# CI Helpers

`downstream-compat.sh` compares the complete Gitcrawl CLI test suite against a
pinned Crawlkit baseline and the candidate checkout. The source pins live in
`ci.yml`. Update pins deliberately, with exact-head review.

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
