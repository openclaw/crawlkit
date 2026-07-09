#!/usr/bin/env bash
set -euo pipefail

VERSION=${1:-}
shift || true
EXPECTED_COMMIT=${1:-}
shift || true
IDENTIFIER=org.openclaw.crawlctl
EXPECTED_AUTHORITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)'
EXPECTED_TEAM_ID=FWJYW4S8P8
EXPECTED_PACKAGE_PATH=github.com/openclaw/crawlkit/cmd/crawlctl
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

verify_build_provenance() {
  local binary=$1 buildinfo
  buildinfo=$(go version -m "$binary")
  [[ "$(grep -Fxc $'\tpath\t'"$EXPECTED_PACKAGE_PATH" <<<"$buildinfo")" == 1 ]] || {
    echo "crawlctl build info contains the wrong Go package path: $binary" >&2
    return 1
  }
  [[ "$(grep -Fxc $'\tbuild\tvcs=git' <<<"$buildinfo")" == 1 ]] || {
    echo "crawlctl build info must identify Git provenance: $binary" >&2
    return 1
  }
  [[ "$(grep -Fxc $'\tbuild\tvcs.revision='"$EXPECTED_COMMIT" <<<"$buildinfo")" == 1 ]] || {
    echo "crawlctl build revision does not match the verified release commit: $binary" >&2
    return 1
  }
  [[ "$(grep -Fxc $'\tbuild\tvcs.modified=false' <<<"$buildinfo")" == 1 ]] || {
    echo "crawlctl build must record vcs.modified=false: $binary" >&2
    return 1
  }
}

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

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ||
  ! "$EXPECTED_COMMIT" =~ ^[0-9a-f]{40}$ || "$#" -eq 0 ]]; then
  echo "usage: $0 vX.Y.Z EXPECTED-COMMIT crawlctl-vX.Y.Z-macos-ARCH.tar.gz [...]" >&2
  exit 2
fi
[[ "$(uname -s)" == Darwin ]] || {
  echo "crawlctl macOS signature verification must run on macOS" >&2
  exit 1
}
for tool in codesign csreq env go lipo shasum tar; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done
EXPECTED_REQUIREMENT_CANONICAL=$(csreq -r "=$REQUIREMENT" -t)

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
  codesign --verify --strict --check-notarization -R=notarized --verbose=2 "$binary"
  signature=$(codesign -dvvv "$binary" 2>&1)
  grep -Fx "Identifier=$IDENTIFIER" <<<"$signature" >/dev/null
  grep -Fx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$signature" >/dev/null
  grep -Fx "Authority=$EXPECTED_AUTHORITY" <<<"$signature" >/dev/null
  grep -F '(runtime)' <<<"$signature" >/dev/null
  verify_designated_requirement "$binary"
  verify_thin_architecture "$binary" "$expected_arch"
  verify_build_provenance "$binary"

  [[ "$(env -i PATH=/usr/bin:/bin "$binary" --version)" == "${VERSION#v}" ]]
done
