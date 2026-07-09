#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION=${1:-}
OUT_DIR=${2:-"$ROOT/dist"}
IDENTIFIER=org.openclaw.crawlctl
EXPECTED_AUTHORITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)'
EXPECTED_TEAM_ID=FWJYW4S8P8
REQUIREMENT="identifier \"$IDENTIFIER\" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = \"$EXPECTED_TEAM_ID\""

usage() {
  echo "usage: $0 vX.Y.Z [output-directory]" >&2
  exit 2
}

[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || usage
[[ "$(uname -s)" == Darwin ]] || {
  echo "crawlctl macOS release packaging must run on macOS" >&2
  exit 1
}
[[ "$(uname -m)" == arm64 ]] || {
  echo "crawlctl release packaging requires Apple Silicon with Rosetta for both architecture smoke tests" >&2
  exit 1
}
[[ -n "${CODESIGN_IDENTITY:-}" ]] || {
  echo "CODESIGN_IDENTITY is required; run this through mac-release codesign-run" >&2
  exit 1
}
[[ "$CODESIGN_IDENTITY" == "$EXPECTED_AUTHORITY" ]] || {
  echo "crawlctl releases require $EXPECTED_AUTHORITY" >&2
  exit 1
}

for tool in codesign git go lipo shasum tar; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done

head_commit=$(git -C "$ROOT" rev-parse HEAD)
tag_commit=$(git -C "$ROOT" rev-parse "refs/tags/$VERSION^{commit}" 2>/dev/null) || {
  echo "release tag does not exist locally: $VERSION" >&2
  exit 1
}
[[ "$head_commit" == "$tag_commit" ]] || {
  echo "HEAD does not match release tag $VERSION" >&2
  exit 1
}
[[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=normal)" ]] || {
  echo "release checkout is not clean" >&2
  exit 1
}
git -C "$ROOT" tag -v "$VERSION" >/dev/null 2>&1 || {
  echo "release tag is not signed by a trusted git signing key: $VERSION" >&2
  exit 1
}

for asset_arch in arm64 x86_64; do
  archive="$OUT_DIR/crawlctl-${VERSION}-macos-${asset_arch}.tar.gz"
  [[ ! -e "$archive" && ! -e "$archive.sha256" ]] || {
    echo "refusing to overwrite existing artifact: $archive" >&2
    exit 1
  }
done

mkdir -p "$OUT_DIR"
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-release.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT

for arch in arm64 amd64; do
  asset_arch=$arch
  [[ "$arch" == amd64 ]] && asset_arch=x86_64
  asset="crawlctl-${VERSION}-macos-${asset_arch}.tar.gz"
  archive="$OUT_DIR/$asset"
  stage="$WORK_DIR/$asset_arch"
  binary="$stage/crawlctl"

  mkdir -p "$stage"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" GOWORK=off \
      go build -trimpath -ldflags "-s -w -X main.version=${VERSION#v}" \
      -o "$binary" ./cmd/crawlctl
  )

  codesign --force --options runtime --timestamp \
    --identifier "$IDENTIFIER" --sign "$CODESIGN_IDENTITY" "$binary"
  codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"

  signature=$(codesign -dvvv "$binary" 2>&1)
  grep -Fx "Identifier=$IDENTIFIER" <<<"$signature" >/dev/null
  grep -Fx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$signature" >/dev/null
  grep -Fx "Authority=$EXPECTED_AUTHORITY" <<<"$signature" >/dev/null
  lipo -archs "$binary" | tr ' ' '\n' | grep -Fx "$asset_arch" >/dev/null

  [[ "$("$binary" --version)" == "${VERSION#v}" ]]

  tar -czf "$archive" -C "$stage" crawlctl
  (
    cd "$OUT_DIR"
    shasum -a 256 "$asset" > "$asset.sha256"
  )
done

"$ROOT/scripts/verify-crawlctl-release.sh" "$VERSION" \
  "$OUT_DIR/crawlctl-${VERSION}-macos-arm64.tar.gz" \
  "$OUT_DIR/crawlctl-${VERSION}-macos-x86_64.tar.gz"
