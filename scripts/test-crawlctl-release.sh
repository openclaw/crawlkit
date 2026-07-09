#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
EXPECTED_AUTHORITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)'
EXPECTED_TEAM_ID=FWJYW4S8P8

fail() {
  echo "release script test failed: $*" >&2
  exit 1
}

for script in install-crawlctl.sh package-crawlctl-release.sh verify-crawlctl-release.sh; do
  bash -n "$ROOT/scripts/$script"
done

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-release-test.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT
FAKE_BIN="$WORK_DIR/bin"
mkdir -p "$FAKE_BIN"

cat > "$FAKE_BIN/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) echo Darwin ;;
  -m) echo arm64 ;;
  *) echo Darwin ;;
esac
EOF

cat > "$FAKE_BIN/git" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == -C ]]; then
  shift 2
fi
case "${1:-}" in
  rev-parse) echo 0123456789abcdef0123456789abcdef01234567 ;;
  status|tag) exit 0 ;;
  *) exit 2 ;;
esac
EOF

cat > "$FAKE_BIN/go" <<'EOF'
#!/usr/bin/env bash
output=
version=
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -o)
      output=$2
      shift 2
      ;;
    -ldflags)
      version=${2##*=}
      shift 2
      ;;
    *) shift ;;
  esac
done
[[ -n "$output" && -n "$version" ]]
{
  echo '#!/usr/bin/env bash'
  echo '[[ "${1:-}" == --version ]] || exit 2'
  printf 'echo %q\n' "$version"
} > "$output"
chmod 0755 "$output"
EOF

cat > "$FAKE_BIN/lipo" <<'EOF'
#!/usr/bin/env bash
case "${2:-}" in
  */x86_64/*) echo x86_64 ;;
  *) echo arm64 ;;
esac
EOF

cat > "$FAKE_BIN/codesign" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${MOCK_CODESIGN_LOG:?}"
case " $* " in
  *' -dvvv '*)
    {
      echo 'Identifier=org.openclaw.crawlctl'
      echo "Authority=${MOCK_CODESIGN_AUTHORITY:-Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)}"
      echo "TeamIdentifier=${MOCK_CODESIGN_TEAM_ID:-FWJYW4S8P8}"
    } >&2
    ;;
esac
EOF

cat > "$FAKE_BIN/curl" <<'EOF'
#!/usr/bin/env bash
output=
url=
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -o)
      output=$2
      shift 2
      ;;
    -w)
      shift 2
      ;;
    -*) shift ;;
    *)
      url=$1
      shift
      ;;
  esac
done
[[ -n "$output" && -n "$url" ]]
cp "${MOCK_ASSET_DIR:?}/${url##*/}" "$output"
EOF

chmod 0755 "$FAKE_BIN"/*
export PATH="$FAKE_BIN:$PATH"
export MOCK_CODESIGN_LOG="$WORK_DIR/codesign.log"

if CODESIGN_IDENTITY='Developer ID Application: Peter Steinberger (Y5PE65HELJ)' \
  bash "$ROOT/scripts/package-crawlctl-release.sh" v0.13.4 "$WORK_DIR/wrong-identity" >/dev/null 2>&1; then
  fail "personal signing identity was accepted"
fi

for version in v0.13.4 v0.13.5; do
  CODESIGN_IDENTITY="$EXPECTED_AUTHORITY" \
    bash "$ROOT/scripts/package-crawlctl-release.sh" "$version" "$WORK_DIR/$version" >/dev/null
  for arch in arm64 x86_64; do
    archive="$WORK_DIR/$version/crawlctl-${version}-macos-${arch}.tar.gz"
    [[ -f "$archive" && -f "$archive.sha256" ]] || fail "missing $version $arch artifact"
  done
done

if MOCK_CODESIGN_AUTHORITY='Developer ID Application: Peter Steinberger (Y5PE65HELJ)' \
  bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.13.4 \
    "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" >/dev/null 2>&1; then
  fail "personal signature was accepted"
fi

INSTALL_DIR="$WORK_DIR/install"
for version in v0.13.4 v0.13.5; do
  MOCK_ASSET_DIR="$WORK_DIR/$version" \
    CRAWLCTL_DOWNLOAD_BASE_URL=https://example.invalid/releases/download/"$version" \
    bash "$ROOT/scripts/install-crawlctl.sh" "$version" "$INSTALL_DIR" >/dev/null
  [[ "$("$INSTALL_DIR/crawlctl" --version)" == "${version#v}" ]] || fail "installed version mismatch"
  if [[ "$version" == v0.13.4 ]]; then
    first_hash=$(shasum -a 256 "$INSTALL_DIR/crawlctl" | awk '{print $1}')
  fi
done
second_hash=$(shasum -a 256 "$INSTALL_DIR/crawlctl" | awk '{print $1}')
[[ "$first_hash" != "$second_hash" ]] || fail "update did not replace the executable"

BAD_ASSETS="$WORK_DIR/bad-assets"
BAD_STAGE="$WORK_DIR/bad-stage"
mkdir -p "$BAD_ASSETS" "$BAD_STAGE"
cp "$INSTALL_DIR/crawlctl" "$BAD_STAGE/crawlctl"
echo unexpected > "$BAD_STAGE/unexpected"
bad_asset=crawlctl-v0.13.6-macos-arm64.tar.gz
tar -czf "$BAD_ASSETS/$bad_asset" -C "$BAD_STAGE" crawlctl unexpected
(
  cd "$BAD_ASSETS"
  shasum -a 256 "$bad_asset" > "$bad_asset.sha256"
)
if MOCK_ASSET_DIR="$BAD_ASSETS" \
  CRAWLCTL_DOWNLOAD_BASE_URL=https://example.invalid/releases/download/v0.13.6 \
  bash "$ROOT/scripts/install-crawlctl.sh" v0.13.6 "$INSTALL_DIR" >/dev/null 2>&1; then
  fail "archive with extra entries was accepted"
fi
[[ "$("$INSTALL_DIR/crawlctl" --version)" == 0.13.5 ]] || fail "failed update changed the installed executable"

grep -F -- '--identifier org.openclaw.crawlctl' "$MOCK_CODESIGN_LOG" >/dev/null || fail "missing fixed identifier"
grep -F "$EXPECTED_TEAM_ID" "$MOCK_CODESIGN_LOG" >/dev/null || fail "missing fixed Team ID requirement"

echo "crawlctl release script tests passed"
