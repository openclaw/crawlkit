#!/usr/bin/env bash
set -euo pipefail

if [[ ${1:-} == --probe-child ]]; then
  exec python3 - <<'PY'
import ctypes
import json
import os
from pathlib import Path
import signal
import time

assert os.getpid() == 1, "cancellation probe requires PID namespace init"
assert os.getuid() == int(os.environ["RUN_UID"]), "probe UID drop failed"
assert os.getgid() == int(os.environ["RUN_GID"]), "probe GID drop failed"
pdeathsig = ctypes.c_int()
PR_GET_PDEATHSIG = 2
assert ctypes.CDLL(None).prctl(PR_GET_PDEATHSIG, ctypes.byref(pdeathsig), 0, 0, 0) == 0, \
    "PR_GET_PDEATHSIG is unavailable"
assert pdeathsig.value == signal.SIGKILL, \
    "setpriv did not preserve unshare's SIGKILL parent-death signal"
os.setsid()
signal.signal(signal.SIGTERM, signal.SIG_IGN)
child = os.fork()
if child == 0:
    os.setsid()
role = "init" if child else "descendant"
marker = {
    "pid": os.getpid(), "uid": os.getuid(), "gid": os.getgid(),
    "namespace": os.readlink("/proc/self/ns/pid"),
    "pdeathsig_before_fork": pdeathsig.value,
}
target = Path(os.environ["PHASE_DIR"]) / (role + ".json")
temporary = target.with_suffix(".tmp")
temporary.write_text(json.dumps(marker))
temporary.replace(target)
while True:
    time.sleep(1)
PY
fi

if [[ ${1:-} == --offline ]]; then
  shift
  ip link set dev lo up
  python3 - <<'PY'
import errno
import socket

with socket.socket() as listener, socket.socket() as client:
    listener.bind(("127.0.0.1", 0))
    listener.listen(1)
    client.settimeout(2)
    client.connect(listener.getsockname())
    connection, _ = listener.accept()
    connection.close()
with socket.socket() as external:
    external.settimeout(2)
    assert external.connect_ex(("192.0.2.1", 443)) in (
        errno.ENETUNREACH, errno.EHOSTUNREACH
    ), "external networking is not isolated"
print("network preflight: loopback available; external route unavailable")
PY
  cd "$PHASE_DIR/source"
  # Changing UID/GID clears PDEATHSIG; restore it before replacing namespace init.
  exec setpriv --reuid="$RUN_UID" --regid="$RUN_GID" --init-groups \
    --pdeathsig keep --no-new-privs "$@"
fi

[[ $(uname -s) == Linux ]] || { echo "Linux is required" >&2; exit 2; }
[[ $# == 3 ]] || { echo "usage: $0 CANDIDATE BASELINE GITCRAWL" >&2; exit 2; }
for tool in go git lsof tar sha256sum sudo unshare ip setpriv timeout python3; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 2; }
done
[[ $(go env GOVERSION) == go1.27.1 ]] || { echo "Go 1.27.1 is required" >&2; exit 2; }
sudo -n true
[[ $(setpriv --help) == *"--pdeathsig"* ]] ||
  { echo "setpriv --pdeathsig keep is required for descendant cleanup" >&2; exit 2; }
setpriv --version
unshare --version
timeout --version

candidate=$(realpath "$1")
baseline=$(realpath "$2")
gitcrawl=$(realpath "$3")
script=$(realpath "$0")
work=$(mktemp -d "${RUNNER_TEMP:?}/crawlkit-downstream.XXXXXX")
mkdir -p "$work/gomodcache" "$work/gopath" "$work/gocache"

verify_checkout() {
  local checkout=$1 expected=$2
  [[ $expected =~ ^[0-9a-f]{40}$ ]]
  [[ $(git -C "$checkout" rev-parse HEAD) == "$expected" ]] ||
    { echo "source head mismatch: $checkout" >&2; exit 1; }
  [[ -z $(git -C "$checkout" status --porcelain) ]] ||
    { echo "source checkout is dirty: $checkout" >&2; exit 1; }
  printf 'source %s tree %s\n' "$expected" "$(git -C "$checkout" rev-parse 'HEAD^{tree}')"
}

verify_checkout "$candidate" "${CRAWLKIT_CANDIDATE_SHA:?}"
verify_checkout "$baseline" "${CRAWLKIT_BASELINE_SHA:?}"
verify_checkout "$gitcrawl" "${GITCRAWL_SHA:?}"
go version
lsof -v 2>&1
sha256sum "$(command -v go)" "$(command -v lsof)"
uname -srmo
printf 'cpus %s\n' "$(getconf _NPROCESSORS_ONLN)"

fingerprint() {
  tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
    -C "$1" -cf - source library | sha256sum
}

phase_env() {
  local phase=$1
  env -i PATH="$PATH" HOME="$phase/home" \
    XDG_CONFIG_HOME="$phase/config" XDG_CACHE_HOME="$phase/cache" \
    XDG_DATA_HOME="$phase/data" XDG_STATE_HOME="$phase/state" \
    XDG_RUNTIME_DIR="$phase/runtime" \
    TMPDIR="$phase/tmp" TMP="$phase/tmp" TEMP="$phase/tmp" \
    GOWORK=off GOTOOLCHAIN=local GOMAXPROCS=2 \
    GOPATH="$work/gopath" GOMODCACHE="$work/gomodcache" GOCACHE="$work/gocache" \
    GOSUMDB=sum.golang.org GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    GIT_NO_LAZY_FETCH=1 "${@:2}"
}

run_isolated() {
  local phase=$1 duration=$2 kill_after=$3
  shift 3
  # timeout must share unshare's privileges to kill its blocked supervisor.
  sudo -n timeout --signal=TERM --kill-after="$kill_after" "$duration" \
    unshare --net --pid --fork --mount-proc --kill-child=KILL \
    env -i PATH="$PATH" HOME="$phase/home" \
    XDG_CONFIG_HOME="$phase/config" XDG_CACHE_HOME="$phase/cache" \
    XDG_DATA_HOME="$phase/data" XDG_STATE_HOME="$phase/state" \
    XDG_RUNTIME_DIR="$phase/runtime" \
    TMPDIR="$phase/tmp" TMP="$phase/tmp" TEMP="$phase/tmp" \
    GOWORK=off GOPROXY=off GOTOOLCHAIN=local GOMAXPROCS=2 \
    GOPATH="$work/gopath" GOMODCACHE="$work/gomodcache" GOCACHE="$work/gocache" \
    GOSUMDB=sum.golang.org GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    GIT_NO_LAZY_FETCH=1 GOFLAGS="-modfile=$phase/modules/go.mod -mod=readonly -p=2" \
    RUN_UID="$(id -u)" RUN_GID="$(id -g)" PHASE_DIR="$phase" \
    bash "$script" --offline "$@"
}

probe="$work/cancellation"
mkdir -p "$probe"/{source,home,config,cache,data,state,tmp}
mkdir -m 700 "$probe/runtime"
run_isolated "$probe" 8s 1s bash "$script" --probe-child > "$probe/output.log" 2>&1 &
probe_job=$!
if phase_env "$probe" python3 - "$probe" <<'PY'
import json
import os
from pathlib import Path
import select
import signal
import sys
import time

root = Path(sys.argv[1])
handles = {}
try:
    assert hasattr(os, "pidfd_open") and hasattr(signal, "pidfd_send_signal"), \
        "Python pidfd support is required for owned-process cleanup"
    ready = time.monotonic() + 5
    while not all((root / (role + ".json")).exists() for role in ("init", "descendant")):
        assert time.monotonic() < ready, "probe did not start; inspect cancellation/output.log"
        time.sleep(0.05)
    markers = [json.loads((root / (role + ".json")).read_text())
               for role in ("init", "descendant")]
    namespace = markers[0]["namespace"]
    assert namespace != os.readlink("/proc/self/ns/pid"), "probe shares host PID namespace"
    assert all(m["namespace"] == namespace and m["uid"] == os.getuid()
               and m["gid"] == os.getgid() for m in markers), "probe ownership mismatch"
    expected = {m["pid"] for m in markers}
    owned = []
    for proc in Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        try:
            if os.readlink(proc / "ns/pid") != namespace:
                continue
            status = dict(line.split(":", 1) for line in (proc / "status").read_text().splitlines())
            inner = int(status["NSpid"].split()[-1])
            assert inner in expected, "unexpected process in owned probe namespace"
            pid = int(proc.name)
            fd = os.pidfd_open(pid)
            try:
                assert os.readlink(proc / "ns/pid") == namespace, "probe PID identity changed"
            except BaseException:
                os.close(fd)
                raise
            handles[fd] = pid
            owned.append({"host_pid": pid, "namespace_pid": inner, "namespace": namespace})
        except (FileNotFoundError, PermissionError):
            continue
    assert {p["namespace_pid"] for p in owned} == expected, \
        "cannot resolve both owned probe PIDs from host /proc"
    (root / "owned-pids.json").write_text(json.dumps(owned, indent=2) + "\n")
    print(json.dumps({"owned_probe_pids": owned}), flush=True)
    poll = select.poll()
    for fd in handles:
        poll.register(fd, select.POLLIN)
    pending = set(handles)
    deadline = time.monotonic() + 10
    while pending and time.monotonic() < deadline:
        pending.difference_update(fd for fd, events in poll.poll(100)
                                  if events & (select.POLLIN | select.POLLHUP))
    assert not pending, "cancellation left an owned namespace process alive"
    print("cancellation probe: namespace init and detached descendant exited")
finally:
    # pidfds can signal only the exact owned processes, never reused numeric PIDs.
    drain = select.poll()
    for fd in handles:
        try:
            signal.pidfd_send_signal(fd, signal.SIGKILL)
        except ProcessLookupError:
            pass
        drain.register(fd, select.POLLIN)
    pending = set(handles)
    deadline = time.monotonic() + 3
    while pending and time.monotonic() < deadline:
        pending.difference_update(fd for fd, events in drain.poll(100)
                                  if events & (select.POLLIN | select.POLLHUP))
    for fd in handles:
        os.close(fd)
    assert not pending, "failed to drain pidfd-bound probe processes"
PY
then
  probe_verified=1
else
  probe_verified=0
fi
if wait "$probe_job"; then
  probe_status=0
else
  probe_status=$?
fi
cat "$probe/output.log"
[[ $probe_verified == 1 && $probe_status == 137 ]] ||
  { echo "cancellation probe failed (exit $probe_status); refusing Go phases" >&2; exit 1; }
printf 'cancellation probe: PASS (native KILL exit %s)\n' "$probe_status"

for name in baseline candidate; do
  phase="$work/$name"
  library=$baseline
  [[ $name == baseline ]] || library=$candidate
  mkdir -p "$phase"/{source,library,modules,home,config,cache,data,state,tmp}
  mkdir -m 700 "$phase/runtime"
  git -C "$gitcrawl" archive HEAD | tar -xf - -C "$phase/source"
  git -C "$library" archive HEAD | tar -xf - -C "$phase/library"
  fingerprint "$phase" > "$phase/source.sha256"
  cp "$phase/source/go.mod" "$phase/modules/go.mod"
  sort -u "$phase/source/go.sum" "$phase/library/go.sum" > "$phase/modules/go.sum"
  (
    cd "$phase/source"
    phase_env "$phase" GOPROXY=https://proxy.golang.org \
      GOFLAGS="-modfile=$phase/modules/go.mod -mod=mod -p=2" \
      go mod edit "-replace=github.com/openclaw/crawlkit=$phase/library"
    # Normalize only private test inputs for the linked library's requirements.
    phase_env "$phase" GOPROXY=https://proxy.golang.org \
      GOFLAGS="-modfile=$phase/modules/go.mod -mod=mod -p=2" \
      go list -deps -test ./internal/cli > "$phase/packages.txt"
    phase_env "$phase" GOPROXY=off \
      GOFLAGS="-modfile=$phase/modules/go.mod -mod=readonly -p=2" \
      go list -m -json github.com/openclaw/crawlkit
    phase_env "$phase" GOPROXY=off \
      GOFLAGS="-modfile=$phase/modules/go.mod -mod=readonly -p=2" \
      go list -deps -test -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' \
      ./internal/cli | sort -u > "$phase/modules.txt"
  )
  sha256sum "$phase/modules/go.mod" "$phase/modules/go.sum" > "$phase/modules.sha256"
  printf '%s dependency inputs:\n' "$name"
  cat "$phase/modules.txt" "$phase/modules.sha256" "$phase/source.sha256"
done

result=0
for name in baseline candidate; do
  phase="$work/$name"
  printf '::group::%s unchanged CLI suite\n' "$name"
  if run_isolated "$phase" 6m 10s go test -count=1 -timeout=3m ./internal/cli \
    2>&1 | tee "$phase/tests.log"; then
    status=0
  else
    status=$?
    result=1
  fi
  printf '::endgroup::\n%s CLI exit: %s\n' "$name" "$status"
  fingerprint "$phase" > "$phase/source-after.sha256"
  cmp "$phase/source.sha256" "$phase/source-after.sha256"
  sha256sum --check "$phase/modules.sha256"
  sha256sum "$phase/tests.log"
  printf '%s CLI exit: %s\n' "$name" "$status" >> "${GITHUB_STEP_SUMMARY:?}"
done

verify_checkout "$candidate" "$CRAWLKIT_CANDIDATE_SHA"
verify_checkout "$baseline" "$CRAWLKIT_BASELINE_SHA"
verify_checkout "$gitcrawl" "$GITCRAWL_SHA"
exit "$result"
