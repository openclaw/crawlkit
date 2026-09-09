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
