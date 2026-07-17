#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION=${1:-}
OUT_DIR=${2:-}

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ || -z "$OUT_DIR" ]]; then
  echo "usage: $0 vX.Y.Z output-directory" >&2
  exit 2
fi
[[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=normal)" ]] || {
  echo "release preflight checkout is not clean" >&2
  exit 1
}

EXPECTED_COMMIT=$(git -C "$ROOT" rev-parse HEAD)
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
