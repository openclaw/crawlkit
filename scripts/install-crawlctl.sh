#!/usr/bin/env bash
set -euo pipefail

VERSION=${1:-latest}
INSTALL_DIR=${2:-}
REPOSITORY=${CRAWLCTL_REPOSITORY:-openclaw/crawlkit}
IDENTIFIER=org.openclaw.crawlctl
EXPECTED_AUTHORITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)'
EXPECTED_TEAM_ID=FWJYW4S8P8
REQUIREMENT="identifier \"$IDENTIFIER\" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = \"$EXPECTED_TEAM_ID\""

verify_designated_requirement() {
  local binary=$1 requirement_output actual_requirement actual_canonical
  requirement_output=$(codesign -d -r- "$binary" 2>&1)
  actual_requirement=$(sed -n 's/^designated => //p' <<<"$requirement_output")
  [[ -n "$actual_requirement" && "$actual_requirement" != *$'\n'* ]] || {
    echo "crawlctl must contain exactly one designated requirement: $binary" >&2
    return 1
  }
  actual_canonical=$(csreq -r "=$actual_requirement" -t)
  [[ "$actual_canonical" == "$EXPECTED_REQUIREMENT_CANONICAL" ]] || {
    echo "crawlctl embedded designated requirement does not match release policy: $binary" >&2
    return 1
  }
}

verify_thin_architecture() {
  local binary=$1 expected_arch=$2 architecture_output
  local -a slices
  architecture_output=$(lipo -archs "$binary")
  architecture_output=${architecture_output//$'\n'/ }
  read -r -a slices <<<"$architecture_output"
  if [[ "${#slices[@]}" -ne 1 || "${slices[0]}" != "$expected_arch" ]]; then
    echo "crawlctl must contain exactly one $expected_arch architecture slice: $binary" >&2
    return 1
  fi
}

verify_checksum() {
  local archive_path=$1 checksum_path=$2 expected_name matches expected_hash actual_hash
  expected_name=$(basename "$archive_path")
  matches=$(awk -v name="$expected_name" '$2 == name || $2 == "*" name { print $1 }' "$checksum_path")
  [[ -n "$matches" && "$matches" != *$'\n'* && "$matches" =~ ^[[:xdigit:]]{64}$ ]] || {
    echo "checksums file must contain exactly one valid record for $expected_name" >&2
    return 1
  }
  expected_hash=$matches
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
  x86_64) arch=amd64 ;;
  *)
    echo "unsupported macOS architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

for tool in awk codesign csreq curl env lipo shasum tar; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done
EXPECTED_REQUIREMENT_CANONICAL=$(csreq -r "=$REQUIREMENT" -t)

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

release_version=${VERSION#v}
asset="crawlkit_${release_version}_darwin_${arch}.tar.gz"
base_url=${CRAWLCTL_DOWNLOAD_BASE_URL:-"https://github.com/$REPOSITORY/releases/download/$VERSION"}
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-install.XXXXXX")
temp_binary=
cleanup() {
  rm -rf "$WORK_DIR"
  [[ -z "$temp_binary" ]] || rm -f "$temp_binary"
}
trap cleanup EXIT

curl -fsSL "$base_url/$asset" -o "$WORK_DIR/$asset"
curl -fsSL "$base_url/checksums.txt" -o "$WORK_DIR/checksums.txt"
verify_checksum "$WORK_DIR/$asset" "$WORK_DIR/checksums.txt"
members=$(tar -tzf "$WORK_DIR/$asset" | sed 's#^\./##; /^$/d')
[[ "$members" == crawlctl ]] || {
  echo "release archive must contain only crawlctl" >&2
  exit 1
}

binary="$WORK_DIR/crawlctl"
tar -xzf "$WORK_DIR/$asset" -C "$WORK_DIR"
chmod 0755 "$binary"
codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"
codesign --verify --strict --check-notarization -R=notarized --verbose=2 "$binary"
signature=$(codesign -dvvv "$binary" 2>&1)
grep -Fx "Identifier=$IDENTIFIER" <<<"$signature" >/dev/null
grep -Fx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$signature" >/dev/null
grep -Fx "Authority=$EXPECTED_AUTHORITY" <<<"$signature" >/dev/null
grep -F '(runtime)' <<<"$signature" >/dev/null
verify_designated_requirement "$binary"
expected_macho_arch=$arch
[[ "$expected_macho_arch" == amd64 ]] && expected_macho_arch=x86_64
verify_thin_architecture "$binary" "$expected_macho_arch"
[[ "$(env -i PATH=/usr/bin:/bin "$binary" --version)" == "${VERSION#v}" ]]

mkdir -p "$INSTALL_DIR"
temp_binary=$(mktemp "$INSTALL_DIR/.crawlctl.XXXXXX")
install -m 0755 "$binary" "$temp_binary"
codesign --verify --strict -R="$REQUIREMENT" "$temp_binary"
codesign --verify --strict --check-notarization -R=notarized "$temp_binary"
verify_designated_requirement "$temp_binary"
mv -f "$temp_binary" "$INSTALL_DIR/crawlctl"
temp_binary=

echo "installed crawlctl ${VERSION#v} at $INSTALL_DIR/crawlctl"
