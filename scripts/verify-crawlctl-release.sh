#!/usr/bin/env bash
set -euo pipefail

VERSION=${1:-}
shift || true
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

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ || "$#" -eq 0 ]]; then
  echo "usage: $0 vX.Y.Z crawlctl-vX.Y.Z-macos-ARCH.tar.gz [...]" >&2
  exit 2
fi
[[ "$(uname -s)" == Darwin ]] || {
  echo "crawlctl macOS signature verification must run on macOS" >&2
  exit 1
}

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-verify.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT

for archive in "$@"; do
  archive=$(cd "$(dirname "$archive")" && pwd)/$(basename "$archive")
  checksum="$archive.sha256"
  [[ -f "$archive" && -f "$checksum" ]] || {
    echo "missing artifact or checksum: $archive" >&2
    exit 1
  }

  case "$(basename "$archive")" in
    "crawlctl-${VERSION}-macos-arm64.tar.gz") expected_arch=arm64 ;;
    "crawlctl-${VERSION}-macos-x86_64.tar.gz") expected_arch=x86_64 ;;
    *)
      echo "unexpected crawlctl artifact name: $(basename "$archive")" >&2
      exit 1
      ;;
  esac

  verify_checksum "$archive" "$checksum"
  [[ "$(tar -tzf "$archive")" == crawlctl ]] || {
    echo "release archive must contain only crawlctl: $archive" >&2
    exit 1
  }

  stage="$WORK_DIR/$expected_arch"
  mkdir -p "$stage"
  binary="$stage/crawlctl"
  tar -xOf "$archive" crawlctl > "$binary"
  chmod 0755 "$binary"

  codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"
  signature=$(codesign -dvvv "$binary" 2>&1)
  grep -Fx "Identifier=$IDENTIFIER" <<<"$signature" >/dev/null
  grep -Fx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$signature" >/dev/null
  grep -Fx "Authority=$EXPECTED_AUTHORITY" <<<"$signature" >/dev/null
  lipo -archs "$binary" | tr ' ' '\n' | grep -Fx "$expected_arch" >/dev/null

  [[ "$("$binary" --version)" == "${VERSION#v}" ]]
done
