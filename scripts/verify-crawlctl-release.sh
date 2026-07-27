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
  local archive=$1 checksum_file=$2 name matches expected_hash actual_hash
  name=$(basename "$archive")
  matches=$(awk -v name="$name" '$2 == name || $2 == "*" name { print $1 }' "$checksum_file")
  [[ -n "$matches" && "$matches" != *$'\n'* && "$matches" =~ ^[[:xdigit:]]{64}$ ]] || {
    echo "checksums file must contain exactly one valid record for $name" >&2
    return 1
  }
  expected_hash=$matches
  actual_hash=$(shasum -a 256 "$archive" | awk '{print $1}')
  [[ "$actual_hash" == "$expected_hash" ]] || {
    echo "checksum mismatch: $archive" >&2
    return 1
  }
}

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ||
  ! "$EXPECTED_COMMIT" =~ ^[0-9a-f]{40}$ || "$#" -eq 0 ]]; then
  echo "usage: $0 X.Y.Z EXPECTED-COMMIT crawlkit_X.Y.Z_PLATFORM_ARCH.tar.gz [...]" >&2
  exit 2
fi
[[ "$(uname -s)" == Darwin ]] || {
  echo "crawlctl release verification must run on macOS" >&2
  exit 1
}
for tool in awk codesign csreq env file go lipo sed shasum tar; do
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
  checksum_file="$(dirname "$archive")/checksums.txt"
  [[ -f "$archive" && -f "$checksum_file" ]] || {
    echo "missing artifact or checksums.txt: $archive" >&2
    exit 1
  }

  platform=
  expected_arch=
  case "$(basename "$archive")" in
    "crawlkit_${VERSION}_darwin_arm64.tar.gz") platform=darwin; expected_arch=arm64 ;;
    "crawlkit_${VERSION}_darwin_amd64.tar.gz") platform=darwin; expected_arch=x86_64 ;;
    "crawlkit_${VERSION}_linux_arm64.tar.gz") platform=linux; expected_arch=arm64 ;;
    "crawlkit_${VERSION}_linux_amd64.tar.gz") platform=linux; expected_arch=amd64 ;;
    *)
      echo "unexpected crawlkit artifact name: $(basename "$archive")" >&2
      exit 1
      ;;
  esac

  verify_checksum "$archive" "$checksum_file"
  members=$(tar -tzf "$archive" | sed 's#^\./##; /^$/d')
  [[ "$members" == crawlctl ]] || {
    echo "release archive must contain only crawlctl: $archive" >&2
    exit 1
  }

  stage="$WORK_DIR/${platform}-${expected_arch}"
  mkdir -p "$stage"
  tar -xzf "$archive" -C "$stage"
  binary="$stage/crawlctl"
  chmod 0755 "$binary"
  file_output=$(file -b "$binary")

  if [[ "$platform" == darwin ]]; then
    [[ "$file_output" == *"Mach-O 64-bit executable"* && "$file_output" == *"$expected_arch"* ]] || {
      echo "unexpected Mach-O architecture for $archive: $file_output" >&2
      exit 1
    }
    codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"
    codesign --verify --strict --check-notarization -R=notarized --verbose=2 "$binary"
    signature=$(codesign -dvvv "$binary" 2>&1)
    grep -Fx "Identifier=$IDENTIFIER" <<<"$signature" >/dev/null
    grep -Fx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$signature" >/dev/null
    grep -Fx "Authority=$EXPECTED_AUTHORITY" <<<"$signature" >/dev/null
    grep -F '(runtime)' <<<"$signature" >/dev/null
    verify_designated_requirement "$binary"
    [[ "$(lipo -archs "$binary")" == "$expected_arch" ]]
  elif [[ "$expected_arch" == arm64 ]]; then
    [[ "$file_output" == *"ELF 64-bit"* && "$file_output" == *"ARM aarch64"* ]] || {
      echo "unexpected Linux arm64 binary: $file_output" >&2
      exit 1
    }
  else
    [[ "$file_output" == *"ELF 64-bit"* && "$file_output" == *"x86-64"* ]] || {
      echo "unexpected Linux amd64 binary: $file_output" >&2
      exit 1
    }
  fi

  verify_build_provenance "$binary"
done
