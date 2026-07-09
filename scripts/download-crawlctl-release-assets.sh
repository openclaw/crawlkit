#!/usr/bin/env bash
set -euo pipefail

TAG=${1:-}
ARCH=${2:-}
EXPECTED_DRAFT=${3:-}
OUT_DIR=${4:-}
REPOSITORY=${GITHUB_REPOSITORY:-}

usage() {
  echo "usage: $0 vX.Y.Z arm64|x86_64 true|false output-directory" >&2
  exit 2
}

[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || usage
[[ "$ARCH" == arm64 || "$ARCH" == x86_64 ]] || usage
[[ "$EXPECTED_DRAFT" == true || "$EXPECTED_DRAFT" == false ]] || usage
[[ -n "$OUT_DIR" && "$REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || usage
[[ -n "${GH_TOKEN:-}" ]] || {
  echo "GH_TOKEN is required" >&2
  exit 1
}

for tool in gh jq; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-release-download.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT

gh api --paginate "repos/$REPOSITORY/releases?per_page=100" > "$WORK_DIR/release-pages.json"
release=$(
  jq -cs --arg tag "$TAG" --argjson draft "$EXPECTED_DRAFT" \
    '[.[][] | select(.tag_name == $tag and .draft == $draft)]' \
    "$WORK_DIR/release-pages.json"
)
[[ "$(jq 'length' <<<"$release")" == 1 ]] || {
  echo "expected exactly one release for $TAG with draft=$EXPECTED_DRAFT" >&2
  exit 1
}
release_id=$(jq -r '.[0].id' <<<"$release")
[[ "$release_id" =~ ^[0-9]+$ ]] || {
  echo "release has an invalid API id" >&2
  exit 1
}

gh api --paginate "repos/$REPOSITORY/releases/$release_id/assets?per_page=100" > "$WORK_DIR/asset-pages.json"
assets=$(jq -cs '[.[][]]' "$WORK_DIR/asset-pages.json")
expected_names=(
  "crawlctl-${TAG}-macos-arm64.tar.gz"
  "crawlctl-${TAG}-macos-arm64.tar.gz.sha256"
  "crawlctl-${TAG}-macos-x86_64.tar.gz"
  "crawlctl-${TAG}-macos-x86_64.tar.gz.sha256"
)
[[ "$(jq 'length' <<<"$assets")" == "${#expected_names[@]}" ]] || {
  echo "release must contain exactly four crawlctl assets" >&2
  exit 1
}

mkdir -p "$OUT_DIR"
api_prefix="https://api.github.com/repos/$REPOSITORY/releases/assets/"
for name in "${expected_names[@]}"; do
  matches=$(jq -c --arg name "$name" '[.[] | select(.name == $name)]' <<<"$assets")
  [[ "$(jq 'length' <<<"$matches")" == 1 ]] || {
    echo "release asset missing or duplicated: $name" >&2
    exit 1
  }
  [[ "$name" == *"-macos-${ARCH}.tar.gz"* ]] || continue
  api_url=$(jq -r '.[0].url' <<<"$matches")
  asset_id=${api_url#"$api_prefix"}
  [[ "$api_url" == "$api_prefix"* && "$asset_id" =~ ^[0-9]+$ ]] || {
    echo "release asset has an invalid API URL: $name" >&2
    exit 1
  }
  gh api "$api_url" -H "Accept: application/octet-stream" > "$WORK_DIR/$name"
  [[ -s "$WORK_DIR/$name" ]] || {
    echo "downloaded release asset is empty: $name" >&2
    exit 1
  }
  mv "$WORK_DIR/$name" "$OUT_DIR/$name"
done
