#!/usr/bin/env bash
set -euo pipefail

VERSION=${1:-latest}
INSTALL_DIR=${2:-}
REPOSITORY=${CRAWLCTL_REPOSITORY:-openclaw/crawlkit}
IDENTIFIER=org.openclaw.crawlctl
EXPECTED_AUTHORITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)'
EXPECTED_TEAM_ID=FWJYW4S8P8
REQUIREMENT="identifier \"$IDENTIFIER\" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = \"$EXPECTED_TEAM_ID\""

verify_checksum() {
  local archive_path=$1 checksum_path=$2 expected_hash expected_name extra actual_hash
  [[ "$(wc -l < "$checksum_path" | tr -d ' ')" == 1 ]] || {
    echo "invalid checksum file: $checksum_path" >&2
    return 1
  }
  read -r expected_hash expected_name extra < "$checksum_path"
  [[ "$expected_hash" =~ ^[[:xdigit:]]{64}$ && "$expected_name" == "$(basename "$archive_path")" && -z "${extra:-}" ]] || {
    echo "invalid checksum record: $checksum_path" >&2
    return 1
  }
  actual_hash=$(shasum -a 256 "$archive_path" | awk '{print $1}')
  [[ "$actual_hash" == "$expected_hash" ]] || {
    echo "checksum mismatch: $archive_path" >&2
    return 1
  }
}

case "$(uname -s)" in
  Darwin) ;;
  *)
    echo "prebuilt crawlctl installation currently supports macOS only" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  arm64) arch=arm64 ;;
  x86_64) arch=x86_64 ;;
  *)
    echo "unsupported macOS architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

if [[ -z "$INSTALL_DIR" ]]; then
  existing=$(command -v crawlctl 2>/dev/null || true)
  if [[ "$existing" == /* && -f "$existing" ]]; then
    INSTALL_DIR=$(dirname "$existing")
  elif command -v go >/dev/null 2>&1; then
    INSTALL_DIR=$(go env GOBIN)
    if [[ -z "$INSTALL_DIR" ]]; then
      go_path=$(go env GOPATH)
      INSTALL_DIR=${go_path%%:*}/bin
    fi
  else
    INSTALL_DIR="$HOME/go/bin"
  fi
fi

if [[ "$VERSION" == latest ]]; then
  effective_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
    "https://github.com/$REPOSITORY/releases/latest")
  VERSION=${effective_url##*/}
fi
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || {
  echo "invalid crawlctl version: $VERSION" >&2
  exit 2
}

asset="crawlctl-${VERSION}-macos-${arch}.tar.gz"
base_url=${CRAWLCTL_DOWNLOAD_BASE_URL:-"https://github.com/$REPOSITORY/releases/download/$VERSION"}
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-install.XXXXXX")
temp_binary=
cleanup() {
  rm -rf "$WORK_DIR"
  [[ -z "$temp_binary" ]] || rm -f "$temp_binary"
}
trap cleanup EXIT

curl -fsSL "$base_url/$asset" -o "$WORK_DIR/$asset"
curl -fsSL "$base_url/$asset.sha256" -o "$WORK_DIR/$asset.sha256"
verify_checksum "$WORK_DIR/$asset" "$WORK_DIR/$asset.sha256"
[[ "$(tar -tzf "$WORK_DIR/$asset")" == crawlctl ]] || {
  echo "release archive must contain only crawlctl" >&2
  exit 1
}

binary="$WORK_DIR/crawlctl"
tar -xOf "$WORK_DIR/$asset" crawlctl > "$binary"
chmod 0755 "$binary"
codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"
signature=$(codesign -dvvv "$binary" 2>&1)
grep -Fx "Identifier=$IDENTIFIER" <<<"$signature" >/dev/null
grep -Fx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$signature" >/dev/null
grep -Fx "Authority=$EXPECTED_AUTHORITY" <<<"$signature" >/dev/null
[[ "$("$binary" --version)" == "${VERSION#v}" ]]

mkdir -p "$INSTALL_DIR"
temp_binary=$(mktemp "$INSTALL_DIR/.crawlctl.XXXXXX")
install -m 0755 "$binary" "$temp_binary"
codesign --verify --strict -R="$REQUIREMENT" "$temp_binary"
mv -f "$temp_binary" "$INSTALL_DIR/crawlctl"
temp_binary=

echo "installed crawlctl ${VERSION#v} at $INSTALL_DIR/crawlctl"
