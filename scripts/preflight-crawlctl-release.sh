#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION=${1:-}
OUT_DIR=${2:-}
REMOTE=origin
DEFAULT_BRANCH=main

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ || -z "$OUT_DIR" ]]; then
  echo "usage: $0 vX.Y.Z output-directory" >&2
  exit 2
fi
for tool in awk git; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done
export GIT_NO_REPLACE_OBJECTS=1
replacement_refs=$(git -C "$ROOT" for-each-ref --format='%(refname)' refs/replace/)
[[ -z "$replacement_refs" ]] || {
  echo "release preflight rejects Git replacement refs" >&2
  exit 1
}
if ! worktree_state=$(git -C "$ROOT" status --porcelain --untracked-files=normal); then
  echo "could not inspect release preflight checkout" >&2
  exit 1
fi
[[ -z "$worktree_state" ]] || {
  echo "release preflight checkout is not clean" >&2
  exit 1
}

remote_url=$(git -C "$ROOT" remote get-url "$REMOTE")
case "$remote_url" in
  https://github.com/openclaw/crawlkit | https://github.com/openclaw/crawlkit.git | \
    git@github.com:openclaw/crawlkit | git@github.com:openclaw/crawlkit.git | \
    ssh://git@github.com/openclaw/crawlkit | ssh://git@github.com/openclaw/crawlkit.git) ;;
  *)
    echo "release preflight requires the official openclaw/crawlkit origin" >&2
    exit 1
    ;;
esac
remote_default_ref=$(git -C "$ROOT" ls-remote --symref "$REMOTE" HEAD |
  awk '$1 == "ref:" && $3 == "HEAD" { print $2 }')
[[ "$remote_default_ref" == "refs/heads/$DEFAULT_BRANCH" ]] || {
  echo "official release origin default branch is not $DEFAULT_BRANCH" >&2
  exit 1
}

ref_namespace="refs/crawlctl-release-preflight/$$-$RANDOM"
branch_ref="$ref_namespace/branch"
cleanup() {
  git -C "$ROOT" update-ref -d "$branch_ref" >/dev/null 2>&1 || true
}
trap cleanup EXIT
git -C "$ROOT" \
  -c fetch.prune=false -c fetch.pruneTags=false \
  -c remote.origin.prune=false -c remote.origin.pruneTags=false \
  fetch --force --no-tags --no-write-fetch-head "$REMOTE" \
  "+refs/heads/$DEFAULT_BRANCH:$branch_ref" >/dev/null
EXPECTED_COMMIT=$(git -C "$ROOT" rev-parse "$branch_ref^{commit}")
[[ "$(git -C "$ROOT" rev-parse HEAD)" == "$EXPECTED_COMMIT" ]] || {
  echo "release preflight HEAD does not match protected $DEFAULT_BRANCH" >&2
  exit 1
}

helper=${MAC_RELEASE_HELPER:-"$HOME/Projects/agent-scripts/skills/release-mac-app/scripts/mac-release"}
[[ -x "$helper" ]] || {
  echo "mac-release helper is not executable: $helper" >&2
  exit 1
}

CRAWLCTL_EXPECTED_COMMIT="$EXPECTED_COMMIT" \
  "$helper" codesign-run --with-package-secrets -- \
  "$ROOT/scripts/package-crawlctl-release.sh" --produce "$VERSION" "$OUT_DIR"

"$ROOT/scripts/verify-crawlctl-release.sh" "$VERSION" "$EXPECTED_COMMIT" \
  "$OUT_DIR/crawlctl-${VERSION}-macos-arm64.tar.gz" \
  "$OUT_DIR/crawlctl-${VERSION}-macos-x86_64.tar.gz"

printf '%s\n' "$EXPECTED_COMMIT"
