#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SCRIPT_PATH="$ROOT/scripts/package-crawlctl-release.sh"
MODE=package
if [[ "${1:-}" == --produce ]]; then
  MODE=produce
  shift
fi
VERSION=${1:-}
OUT_DIR=${2:-"$ROOT/dist"}
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
  echo "crawlctl release packaging requires Apple Silicon with Rosetta" >&2
  exit 1
}

if [[ "$MODE" == produce ]]; then
  EXPECTED_COMMIT=${CRAWLCTL_EXPECTED_COMMIT:-}
  [[ "$EXPECTED_COMMIT" =~ ^[0-9a-f]{40}$ ]] || {
    echo "CRAWLCTL_EXPECTED_COMMIT must be the verified remote release commit" >&2
    exit 1
  }
  [[ -n "${CODESIGN_IDENTITY:-}" ]] || {
    echo "CODESIGN_IDENTITY is required; run the package entrypoint normally" >&2
    exit 1
  }
  [[ "$CODESIGN_IDENTITY" == "$EXPECTED_AUTHORITY" ]] || {
    echo "crawlctl releases require $EXPECTED_AUTHORITY" >&2
    exit 1
  }
  [[ -n "${NOTARYTOOL_KEYCHAIN_PROFILE:-}" ]] || {
    echo "NOTARYTOOL_KEYCHAIN_PROFILE is required for crawlctl release notarization" >&2
    exit 1
  }
  for tool in codesign csreq ditto git go jq lipo shasum tar xcrun; do
    command -v "$tool" >/dev/null || {
      echo "missing required tool: $tool" >&2
      exit 1
    }
  done
  EXPECTED_REQUIREMENT_CANONICAL=$(csreq -r "=$REQUIREMENT" -t)
  [[ "$(git -C "$ROOT" rev-parse HEAD)" == "$EXPECTED_COMMIT" ]] || {
    echo "local HEAD changed after remote release provenance verification" >&2
    exit 1
  }
  [[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=normal)" ]] || {
    echo "release checkout changed after remote release provenance verification" >&2
    exit 1
  }

  WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-release-produce.XXXXXX")
  trap 'rm -rf "$WORK_DIR"' EXIT
  STAGED_OUTPUT="$WORK_DIR/output"
  mkdir -p "$STAGED_OUTPUT"
  staged_artifacts=()

  for arch in arm64 amd64; do
    asset_arch=$arch
    [[ "$arch" == amd64 ]] && asset_arch=x86_64
    asset="crawlctl-${VERSION}-macos-${asset_arch}.tar.gz"
    archive="$STAGED_OUTPUT/$asset"
    stage="$WORK_DIR/$asset_arch"
    binary="$stage/crawlctl"
    notary_archive="$stage/crawlctl-${asset_arch}-notary.zip"
    notary_result="$stage/notary-result.json"

    mkdir -p "$stage"
    (
      cd "$ROOT"
      CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" GOWORK=off \
        go build -buildvcs=true -trimpath \
        -ldflags "-s -w -X main.version=${VERSION#v}" \
        -o "$binary" ./cmd/crawlctl
    )

    codesign --force --options runtime --timestamp \
      --identifier "$IDENTIFIER" --sign "$CODESIGN_IDENTITY" "$binary"
    codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"
    signature=$(codesign -dvvv "$binary" 2>&1)
    grep -Fx "Identifier=$IDENTIFIER" <<<"$signature" >/dev/null
    grep -Fx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$signature" >/dev/null
    grep -Fx "Authority=$EXPECTED_AUTHORITY" <<<"$signature" >/dev/null
    grep -F '(runtime)' <<<"$signature" >/dev/null
    verify_designated_requirement "$binary"
    verify_thin_architecture "$binary" "$asset_arch"

    ditto -c -k --keepParent "$binary" "$notary_archive"
    if ! xcrun notarytool submit "$notary_archive" \
      --keychain-profile "$NOTARYTOOL_KEYCHAIN_PROFILE" \
      --no-s3-acceleration --wait --output-format json >"$notary_result"; then
      echo "crawlctl notarization submission failed for $asset_arch" >&2
      exit 1
    fi
    jq -e '.status == "Accepted"' "$notary_result" >/dev/null || {
      echo "crawlctl notarization was not accepted for $asset_arch" >&2
      exit 1
    }
    codesign --verify --strict --check-notarization -R=notarized --verbose=2 "$binary"

    tar -czf "$archive" -C "$stage" crawlctl
    (
      cd "$STAGED_OUTPUT"
      shasum -a 256 "$asset" >"$asset.sha256"
    )
    staged_artifacts+=("$asset" "$asset.sha256")
  done

  for artifact in "${staged_artifacts[@]}"; do
    [[ ! -e "$OUT_DIR/$artifact" ]] || {
      echo "refusing to overwrite staged artifact: $OUT_DIR/$artifact" >&2
      exit 1
    }
  done
  mkdir -p "$OUT_DIR"
  for artifact in "${staged_artifacts[@]}"; do
    mv "$STAGED_OUTPUT/$artifact" "$OUT_DIR/$artifact"
  done
  exit 0
fi

for tool in git mv; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done
[[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=normal)" ]] || {
  echo "release checkout is not clean" >&2
  exit 1
}
EXPECTED_COMMIT=$("$ROOT/scripts/verify-crawlctl-release-provenance.sh" "$VERSION")

OUT_PARENT=$(dirname "$OUT_DIR")
[[ -d "$OUT_PARENT" ]] || {
  echo "output parent directory does not exist: $OUT_PARENT" >&2
  exit 1
}
OUT_PARENT=$(cd "$OUT_PARENT" && pwd)
OUT_DIR="$OUT_PARENT/$(basename "$OUT_DIR")"
artifacts=(
  "crawlctl-${VERSION}-macos-arm64.tar.gz"
  "crawlctl-${VERSION}-macos-arm64.tar.gz.sha256"
  "crawlctl-${VERSION}-macos-x86_64.tar.gz"
  "crawlctl-${VERSION}-macos-x86_64.tar.gz.sha256"
)
for artifact in "${artifacts[@]}"; do
  [[ ! -e "$OUT_DIR/$artifact" ]] || {
    echo "refusing to overwrite existing artifact: $OUT_DIR/$artifact" >&2
    exit 1
  }
done

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-release.XXXXXX")
SIGNED_OUTPUT="$WORK_DIR/signed"
DESTINATION_STAGE=
PROMOTION_ACTIVE=0
OUT_DIR_CREATED=0
promoted_artifacts=()
cleanup() {
  local rc=$?
  set +e
  if [[ "$PROMOTION_ACTIVE" == 1 ]]; then
    for artifact in "${promoted_artifacts[@]}"; do
      rm -f "$OUT_DIR/$artifact"
    done
  fi
  [[ -z "$DESTINATION_STAGE" ]] || rm -rf "$DESTINATION_STAGE"
  if [[ "$rc" -ne 0 && "$OUT_DIR_CREATED" == 1 ]]; then
    rmdir "$OUT_DIR" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK_DIR"
  trap - EXIT
  exit "$rc"
}
trap cleanup EXIT

helper=${MAC_RELEASE_HELPER:-"$HOME/Projects/agent-scripts/skills/release-mac-app/scripts/mac-release"}
[[ -x "$helper" ]] || {
  echo "mac-release helper is not executable: $helper" >&2
  exit 1
}
CRAWLCTL_EXPECTED_COMMIT="$EXPECTED_COMMIT" \
  "$helper" codesign-run --with-package-secrets -- \
  "$SCRIPT_PATH" --produce "$VERSION" "$SIGNED_OUTPUT"

REVALIDATED_COMMIT=$("$ROOT/scripts/verify-crawlctl-release-provenance.sh" "$VERSION")
[[ "$REVALIDATED_COMMIT" == "$EXPECTED_COMMIT" ]] || {
  echo "remote release provenance changed during artifact production" >&2
  exit 1
}

"$ROOT/scripts/verify-crawlctl-release.sh" "$VERSION" "$EXPECTED_COMMIT" \
  "$SIGNED_OUTPUT/crawlctl-${VERSION}-macos-arm64.tar.gz" \
  "$SIGNED_OUTPUT/crawlctl-${VERSION}-macos-x86_64.tar.gz"

[[ ! -L "$OUT_DIR" ]] || {
  echo "release output directory must not be a symbolic link: $OUT_DIR" >&2
  exit 1
}
if [[ ! -d "$OUT_DIR" ]]; then
  mkdir "$OUT_DIR"
  OUT_DIR_CREATED=1
fi
DESTINATION_STAGE=$(mktemp -d "$OUT_DIR/.crawlctl-release-promote.XXXXXX")
for artifact in "${artifacts[@]}"; do
  mv "$SIGNED_OUTPUT/$artifact" "$DESTINATION_STAGE/$artifact"
done
for artifact in "${artifacts[@]}"; do
  [[ ! -e "$OUT_DIR/$artifact" ]] || {
    echo "refusing to overwrite existing artifact: $OUT_DIR/$artifact" >&2
    exit 1
  }
done
PROMOTION_ACTIVE=1
for artifact in "${artifacts[@]}"; do
  mv "$DESTINATION_STAGE/$artifact" "$OUT_DIR/$artifact"
  promoted_artifacts+=("$artifact")
done
PROMOTION_ACTIVE=0
