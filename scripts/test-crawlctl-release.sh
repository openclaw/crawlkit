#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
EXPECTED_AUTHORITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)'
EXPECTED_TEAM_ID=FWJYW4S8P8
WRONG_REQUIREMENT='identifier org.openclaw.other and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = FWJYW4S8P8'

fail() {
  echo "release script test failed: $*" >&2
  exit 1
}

for script in download-crawlctl-release-assets.sh install-crawlctl.sh package-crawlctl-release.sh \
  preflight-crawlctl-release.sh verify-crawlctl-release-provenance.sh \
  verify-crawlctl-release.sh; do
  bash -n "$ROOT/scripts/$script"
done
grep -Fx \
  'steipete@gmail.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA6rFpd7CodTF6fy60LZTriTeiGAJ7haIBWD4hrdxmDB' \
  "$ROOT/.github/release-allowed-signers" >/dev/null
[[ "$(wc -l < "$ROOT/.github/release-allowed-signers" | tr -d ' ')" == 1 ]] ||
  fail "release signer policy must contain exactly one reviewed signer"
grep -F "github.event_name == 'release' ||" \
  "$ROOT/.github/workflows/release-assets.yml" >/dev/null
grep -F "ref: \${{ github.event_name == 'release' && github.event.repository.default_branch || github.workflow_sha }}" \
  "$ROOT/.github/workflows/release-assets.yml" >/dev/null
grep -F "expected_draft=\"\${{ inputs.draft }}\"" \
  "$ROOT/.github/workflows/release-assets.yml" >/dev/null
if grep -F 'NOTARYTOOL_KEYCHAIN_PROFILE' "$ROOT/.github/workflows/release-assets.yml" >/dev/null; then
  fail "protected draft verifier must remain credential-free"
fi
# shellcheck disable=SC2016
grep -F './scripts/package-crawlctl-release.sh "$(VERSION)"' "$ROOT/Makefile" >/dev/null
grep -F 'codesign-run --with-package-secrets --' "$ROOT/scripts/package-crawlctl-release.sh" >/dev/null
# shellcheck disable=SC2016
grep -F 'verify-crawlctl-release-provenance.sh "$RELEASE_TAG"' \
  "$ROOT/.github/workflows/release-assets.yml" >/dev/null
# shellcheck disable=SC2016
grep -F '"$RELEASE_TAG" "$RELEASE_COMMIT" "$archive"' \
  "$ROOT/.github/workflows/release-assets.yml" >/dev/null
grep -F 'gpg.ssh.allowedSignersFile=' \
  "$ROOT/scripts/verify-crawlctl-release-provenance.sh" >/dev/null
grep -F 'export GIT_NO_REPLACE_OBJECTS=1' \
  "$ROOT/scripts/preflight-crawlctl-release.sh" >/dev/null
grep -F -- '-c fetch.prune=false -c fetch.pruneTags=false' \
  "$ROOT/scripts/preflight-crawlctl-release.sh" >/dev/null
grep -F -- '-c remote.origin.prune=false -c remote.origin.pruneTags=false' \
  "$ROOT/scripts/preflight-crawlctl-release.sh" >/dev/null
# shellcheck disable=SC2016
grep -F '+refs/heads/$DEFAULT_BRANCH:$branch_ref' \
  "$ROOT/scripts/verify-crawlctl-release-provenance.sh" >/dev/null
grep -F 'go build -buildvcs=true' "$ROOT/scripts/package-crawlctl-release.sh" >/dev/null
# shellcheck disable=SC2016
grep -F 'go version -m "$binary"' "$ROOT/scripts/verify-crawlctl-release.sh" >/dev/null
# shellcheck disable=SC2016
grep -F 'env -i PATH=/usr/bin:/bin "$binary" --version' \
  "$ROOT/scripts/verify-crawlctl-release.sh" "$ROOT/scripts/install-crawlctl.sh" >/dev/null
grep -Fx '.mac-release.env' "$ROOT/.gitignore" >/dev/null
if git -C "$ROOT" ls-files | grep -Fx '.mac-release.env' >/dev/null; then
  fail "runtime release manifest must remain untracked"
fi
grep -Fx "MAC_RELEASE_CODESIGN_IDENTITY='$EXPECTED_AUTHORITY'" \
  "$ROOT/.mac-release.env.example" >/dev/null
grep -Fx 'MAC_RELEASE_CODESIGN_KEYCHAIN_MANAGED=1' "$ROOT/.mac-release.env.example" >/dev/null
grep -Fx 'MAC_RELEASE_CODESIGN_PASSWORDLESS=1' "$ROOT/.mac-release.env.example" >/dev/null
if grep -Eq '^(MAC_RELEASE_CODESIGN_KEYCHAIN=|MAC_RELEASE_CODESIGN_OP_|MAC_RELEASE_OP_)' \
  "$ROOT/.mac-release.env.example"; then
  fail "release manifest example contains runtime credential routing"
fi
if grep -E 'stapler|spctl' "$ROOT/scripts/package-crawlctl-release.sh" \
  "$ROOT/scripts/verify-crawlctl-release.sh" "$ROOT/scripts/install-crawlctl.sh" >/dev/null; then
  fail "raw crawlctl release flow must not claim stapling or spctl assessment support"
fi

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-release-test.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT
FAKE_BIN="$WORK_DIR/bin"
mkdir -p "$FAKE_BIN"
MOCK_COMMIT=0123456789abcdef0123456789abcdef01234567
MOCK_SIDE_COMMIT=89abcdef0123456789abcdef0123456789abcdef
MOCK_CLEANUP_MARKER="$WORK_DIR/keychain-cleaned"
MOCK_RELEASE_EVENT_LOG="$WORK_DIR/release-events.log"
MOCK_HELPER_LOG="$WORK_DIR/helper.log"
MOCK_GIT_LOG="$WORK_DIR/git.log"
MOCK_DITTO_LOG="$WORK_DIR/ditto.log"
MOCK_FETCHED_TAG_NAME_FILE="$WORK_DIR/fetched-tag-name"
export MOCK_COMMIT MOCK_CLEANUP_MARKER MOCK_RELEASE_EVENT_LOG MOCK_HELPER_LOG MOCK_GIT_LOG \
  MOCK_DITTO_LOG MOCK_FETCHED_TAG_NAME_FILE

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
while [[ "${1:-}" == -c ]]; do
  shift 2
done
case "${1:-}" in
  remote)
    [[ "${2:-}" == get-url && "${3:-}" == origin ]]
    echo "${MOCK_REMOTE_URL:-https://github.com/openclaw/crawlkit.git}"
    ;;
  ls-remote)
    [[ "${2:-}" == --symref && "${3:-}" == origin && "${4:-}" == HEAD ]]
    printf 'ref: refs/heads/%s\tHEAD\n' "${MOCK_DEFAULT_BRANCH:-main}"
    ;;
  fetch)
    printf '%s\n' "$*" >> "${MOCK_GIT_LOG:?}"
    for refspec in "$@"; do
      case "$refspec" in
        +refs/tags/*:*)
          fetched_tag=${refspec#+refs/tags/}
          printf '%s\n' "${fetched_tag%%:*}" > "${MOCK_FETCHED_TAG_NAME_FILE:?}"
          ;;
      esac
    done
    [[ "${MOCK_FETCH_RESULT:-ok}" == ok ]]
    ;;
  cat-file)
    case "${2:-}" in
      -t) echo "${MOCK_TAG_TYPE:-tag}" ;;
      tag)
        tag_name=${MOCK_TAG_NAME:-}
        if [[ -z "$tag_name" && -s "${MOCK_FETCHED_TAG_NAME_FILE:?}" ]]; then
          tag_name=$(/bin/cat "$MOCK_FETCHED_TAG_NAME_FILE")
        fi
        tag_name=${tag_name:-v0.13.4}
        signature_format=${MOCK_TAG_SIGNATURE_FORMAT:-SSH}
        cat <<TAG
object ${MOCK_COMMIT:?}
type commit
tag $tag_name
tagger Release Bot <release@example.com> 0 +0000

Release $tag_name
-----BEGIN $signature_format SIGNATURE-----
mock-signature
-----END $signature_format SIGNATURE-----
TAG
        ;;
      *) exit 2 ;;
    esac
    ;;
  verify-tag)
    [[ "${MOCK_VERIFY_TAG_RESULT:-accepted}" == accepted ]]
    ;;
  rev-parse)
    case "${2:-}" in
      HEAD) echo "${MOCK_LOCAL_HEAD:-${MOCK_COMMIT:?}}" ;;
      */tag\^\{commit\}) echo "${MOCK_TAG_COMMIT:-${MOCK_COMMIT:?}}" ;;
      */branch\^\{commit\}) echo "${MOCK_BRANCH_COMMIT:-${MOCK_COMMIT:?}}" ;;
      *) exit 2 ;;
    esac
    ;;
  status) [[ "${MOCK_STATUS_RESULT:-ok}" == ok ]] ;;
  for-each-ref)
    [[ -z "${MOCK_REPLACE_REF:-}" ]] || echo "$MOCK_REPLACE_REF"
    ;;
  update-ref) exit 0 ;;
  *) exit 2 ;;
esac
EOF

cat > "$FAKE_BIN/go" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == version && "${2:-}" == -m ]]; then
  {
    printf '\tpath\t%s\n' "${MOCK_BUILDINFO_PATH:-github.com/openclaw/crawlkit/cmd/crawlctl}"
    printf '\tbuild\tvcs=git\n'
    printf '\tbuild\tvcs.revision=%s\n' "${MOCK_BUILDINFO_REVISION:-${MOCK_COMMIT:?}}"
    printf '\tbuild\tvcs.modified=%s\n' "${MOCK_BUILDINFO_MODIFIED:-false}"
  }
  exit 0
fi
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
  printf '[[ -f %q ]] || exit 91\n' "${MOCK_CLEANUP_MARKER:?}"
  echo '[[ -z "${GH_TOKEN:-}${GITHUB_TOKEN:-}${NOTARYTOOL_KEYCHAIN_PROFILE:-}${CODESIGN_IDENTITY:-}" ]] || exit 92'
  printf 'printf "candidate\\n" >> %q\n' "${MOCK_RELEASE_EVENT_LOG:?}"
  printf 'echo %q\n' "$version"
} > "$output"
chmod 0755 "$output"
EOF

cat > "$FAKE_BIN/lipo" <<'EOF'
#!/usr/bin/env bash
if [[ -n "${MOCK_LIPO_ARCHS:-}" ]]; then
  echo "$MOCK_LIPO_ARCHS"
  exit 0
fi
case "${2:-}" in
  */x86_64/*) echo x86_64 ;;
  *) echo arm64 ;;
esac
EOF

cat > "$FAKE_BIN/codesign" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${MOCK_CODESIGN_LOG:?}"
if [[ " $* " == *' --check-notarization '* && "${MOCK_NOTARY_TICKET:-accepted}" != accepted ]]; then
  exit 1
fi
case " $* " in
  *' -d -r- '*)
    echo "designated => ${MOCK_DESIGNATED_REQUIREMENT:-identifier org.openclaw.crawlctl and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = FWJYW4S8P8}" >&2
    ;;
  *' -dvvv '*)
    {
      echo 'CodeDirectory v=20500 size=123 flags=0x10000(runtime) hashes=1+2 location=embedded'
      echo 'Identifier=org.openclaw.crawlctl'
      echo "Authority=${MOCK_CODESIGN_AUTHORITY:-Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)}"
      echo "TeamIdentifier=${MOCK_CODESIGN_TEAM_ID:-FWJYW4S8P8}"
    } >&2
    ;;
esac
EOF

cat > "$FAKE_BIN/csreq" <<'EOF'
#!/usr/bin/env bash
[[ "$#" == 3 && "$1" == -r && "$2" == =* && "$3" == -t ]]
printf '%s\n' "${2#=}" | tr -d '"'
EOF

cat > "$FAKE_BIN/ditto" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${MOCK_DITTO_LOG:?}"
[[ "$#" == 5 && "$1" == -c && "$2" == -k && "$3" == --keepParent ]]
[[ -f "$4" && "$5" == *.zip ]]
/bin/cp "$4" "$5"
EOF

cat > "$FAKE_BIN/xcrun" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${MOCK_XCRUN_LOG:?}"
[[ "${1:-}" == notarytool && "${2:-}" == submit ]]
archive=${3:-}
[[ -f "$archive" && "$archive" == *.zip ]]
shift 3
profile=
no_s3=0
wait_for_result=0
json_output=0
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --keychain-profile)
      profile=${2:-}
      shift 2
      ;;
    --no-s3-acceleration)
      no_s3=1
      shift
      ;;
    --wait)
      wait_for_result=1
      shift
      ;;
    --output-format)
      [[ "${2:-}" == json ]] && json_output=1
      shift 2
      ;;
    *) exit 2 ;;
  esac
done
[[ -n "$profile" && "$no_s3" == 1 && "$wait_for_result" == 1 && "$json_output" == 1 ]]
[[ "${MOCK_NOTARY_EXIT:-0}" == 0 ]] || exit 1
printf '{"id":"mock-submission","status":"%s"}\n' "${MOCK_NOTARY_STATUS:-Accepted}"
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

cat > "$FAKE_BIN/gh" <<'EOF'
#!/usr/bin/env bash
[[ "${1:-}" == api ]] || exit 2
shift
if [[ "${1:-}" == --paginate ]]; then
  shift
fi
endpoint=${1:-}
case "$endpoint" in
  repos/*/releases\?per_page=100) cat "${MOCK_GH_RELEASES_JSON:?}" ;;
  repos/*/releases/*/assets\?per_page=100) cat "${MOCK_GH_ASSETS_JSON:?}" ;;
  https://api.github.com/repos/*/releases/assets/*) cat "${MOCK_GH_ASSET_DIR:?}/${endpoint##*/}" ;;
  *) exit 2 ;;
esac
EOF

cat > "$FAKE_BIN/mac-release" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${MOCK_HELPER_LOG:?}"
[[ "${1:-}" == codesign-run && "${2:-}" == --with-package-secrets && "${3:-}" == -- ]]
shift 3
rm -f "${MOCK_CLEANUP_MARKER:?}"
CODESIGN_IDENTITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)' \
  NOTARYTOOL_KEYCHAIN_PROFILE=test-profile "$@"
result=$?
printf 'cleanup\n' >> "${MOCK_RELEASE_EVENT_LOG:?}"
touch "$MOCK_CLEANUP_MARKER"
exit "$result"
EOF

cat > "$FAKE_BIN/mv" <<'EOF'
#!/usr/bin/env bash
destination=${!#}
if [[ -n "${MOCK_FAIL_PROMOTION_DIR:-}" && "$(dirname "$destination")" == "$MOCK_FAIL_PROMOTION_DIR" ]]; then
  count=0
  [[ ! -f "${MOCK_PROMOTION_COUNTER:?}" ]] || count=$(<"$MOCK_PROMOTION_COUNTER")
  count=$((count + 1))
  printf '%s\n' "$count" > "$MOCK_PROMOTION_COUNTER"
  [[ "$count" -lt 2 ]] || exit 70
fi
exec /bin/mv "$@"
EOF

chmod 0755 "$FAKE_BIN"/*
export PATH="$FAKE_BIN:$PATH"
export MOCK_CODESIGN_LOG="$WORK_DIR/codesign.log"
export MOCK_XCRUN_LOG="$WORK_DIR/xcrun.log"
export MAC_RELEASE_HELPER="$FAKE_BIN/mac-release"

verified_commit=$(bash "$ROOT/scripts/verify-crawlctl-release-provenance.sh" v0.13.4)
[[ "$verified_commit" == "$MOCK_COMMIT" ]] || fail "wrong verified remote release commit"
if MOCK_TAG_COMMIT="$MOCK_SIDE_COMMIT" \
  bash "$ROOT/scripts/verify-crawlctl-release-provenance.sh" v0.13.4 >/dev/null 2>&1; then
  fail "signed side-commit release tag matched the protected branch head"
fi
if MOCK_VERIFY_TAG_RESULT=rejected \
  bash "$ROOT/scripts/verify-crawlctl-release-provenance.sh" v0.13.4 >/dev/null 2>&1; then
  fail "release tag outside the repository-pinned signer policy was accepted"
fi
if MOCK_TAG_SIGNATURE_FORMAT=PGP \
  bash "$ROOT/scripts/verify-crawlctl-release-provenance.sh" v0.13.4 >/dev/null 2>&1; then
  fail "non-SSH release tag signature was accepted"
fi
if MOCK_TAG_NAME=v0.13.3 \
  bash "$ROOT/scripts/verify-crawlctl-release-provenance.sh" v0.13.4 >/dev/null 2>&1; then
  fail "signed tag object naming another release was accepted"
fi
if MOCK_REMOTE_URL=https://github.com/steipete/crawlkit.git \
  bash "$ROOT/scripts/verify-crawlctl-release-provenance.sh" v0.13.4 >/dev/null 2>&1; then
  fail "non-official release origin was accepted"
fi
if MOCK_DEFAULT_BRANCH=develop \
  bash "$ROOT/scripts/verify-crawlctl-release-provenance.sh" v0.13.4 >/dev/null 2>&1; then
  fail "non-main official remote default branch was accepted"
fi
grep -F 'refs/tags/v0.13.4:' "$MOCK_GIT_LOG" >/dev/null || fail "remote tag was not fetched"
grep -F 'refs/heads/main:' "$MOCK_GIT_LOG" >/dev/null || fail "protected branch head was not fetched"

if CRAWLCTL_EXPECTED_COMMIT="$MOCK_COMMIT" CODESIGN_IDENTITY="$EXPECTED_AUTHORITY" \
  bash "$ROOT/scripts/package-crawlctl-release.sh" --produce \
    v0.13.4 "$WORK_DIR/missing-profile" >/dev/null 2>&1; then
  fail "missing notarization profile was accepted"
fi
[[ ! -e "$WORK_DIR/missing-profile" ]] || fail "missing profile mutated the artifact destination"

if CRAWLCTL_EXPECTED_COMMIT="$MOCK_COMMIT" \
  CODESIGN_IDENTITY='Developer ID Application: Peter Steinberger (Y5PE65HELJ)' \
  NOTARYTOOL_KEYCHAIN_PROFILE=test-profile \
  bash "$ROOT/scripts/package-crawlctl-release.sh" --produce \
    v0.13.4 "$WORK_DIR/wrong-identity" >/dev/null 2>&1; then
  fail "personal signing identity was accepted"
fi
[[ ! -e "$WORK_DIR/wrong-identity" ]] || fail "wrong identity mutated the artifact destination"

if CRAWLCTL_EXPECTED_COMMIT="$MOCK_COMMIT" CODESIGN_IDENTITY="$EXPECTED_AUTHORITY" \
  NOTARYTOOL_KEYCHAIN_PROFILE=test-profile \
  MOCK_DESIGNATED_REQUIREMENT="$WRONG_REQUIREMENT" \
  bash "$ROOT/scripts/package-crawlctl-release.sh" --produce \
    v0.13.4 "$WORK_DIR/wrong-requirement" >/dev/null 2>&1; then
  fail "mismatched embedded designated requirement was accepted"
fi
[[ ! -e "$WORK_DIR/wrong-requirement" ]] ||
  fail "wrong designated requirement mutated the artifact destination"

if CRAWLCTL_EXPECTED_COMMIT="$MOCK_COMMIT" CODESIGN_IDENTITY="$EXPECTED_AUTHORITY" \
  NOTARYTOOL_KEYCHAIN_PROFILE=test-profile \
  MOCK_LIPO_ARCHS='arm64 x86_64' \
  bash "$ROOT/scripts/package-crawlctl-release.sh" --produce \
    v0.13.4 "$WORK_DIR/universal-package" >/dev/null 2>&1; then
  fail "universal binary was accepted for an architecture-specific release asset"
fi
[[ ! -e "$WORK_DIR/universal-package" ]] ||
  fail "universal binary mutated the artifact destination"

if CRAWLCTL_EXPECTED_COMMIT="$MOCK_COMMIT" CODESIGN_IDENTITY="$EXPECTED_AUTHORITY" \
  NOTARYTOOL_KEYCHAIN_PROFILE=test-profile \
  MOCK_NOTARY_STATUS=Invalid \
  bash "$ROOT/scripts/package-crawlctl-release.sh" --produce \
    v0.13.4 "$WORK_DIR/rejected-submission" >/dev/null 2>&1; then
  fail "rejected notarization submission was accepted"
fi
[[ ! -e "$WORK_DIR/rejected-submission" ]] || fail "rejected submission mutated the artifact destination"

if CRAWLCTL_EXPECTED_COMMIT="$MOCK_COMMIT" CODESIGN_IDENTITY="$EXPECTED_AUTHORITY" \
  NOTARYTOOL_KEYCHAIN_PROFILE=test-profile \
  MOCK_NOTARY_TICKET=missing \
  bash "$ROOT/scripts/package-crawlctl-release.sh" --produce \
    v0.13.4 "$WORK_DIR/missing-ticket" >/dev/null 2>&1; then
  fail "missing online notarization ticket was accepted"
fi
[[ ! -e "$WORK_DIR/missing-ticket" ]] || fail "missing ticket mutated the artifact destination"

for version in v0.13.4 v0.14.0; do
  GH_TOKEN=caller-token GITHUB_TOKEN=caller-token \
    bash "$ROOT/scripts/package-crawlctl-release.sh" "$version" "$WORK_DIR/$version" >/dev/null
  for arch in arm64 x86_64; do
    archive="$WORK_DIR/$version/crawlctl-${version}-macos-${arch}.tar.gz"
    [[ -f "$archive" && -f "$archive.sha256" ]] || fail "missing $version $arch artifact"
  done
  [[ "$(find "$WORK_DIR/$version" -maxdepth 1 -type f | wc -l | tr -d ' ')" == 4 ]] ||
    fail "unexpected release artifact inventory for $version"
done
if MOCK_STATUS_RESULT=fail bash "$ROOT/scripts/preflight-crawlctl-release.sh" \
  v0.14.3 "$WORK_DIR/preflight-status-error" >/dev/null 2>&1; then
  fail "preflight accepted a failed checkout cleanliness probe"
fi
[[ ! -e "$WORK_DIR/preflight-status-error" ]] ||
  fail "failed checkout probe mutated the preflight artifact destination"
if MOCK_REPLACE_REF=refs/replace/$MOCK_COMMIT \
  bash "$ROOT/scripts/preflight-crawlctl-release.sh" \
    v0.14.3 "$WORK_DIR/preflight-replace-ref" >/dev/null 2>&1; then
  fail "preflight accepted a Git replacement ref"
fi
[[ ! -e "$WORK_DIR/preflight-replace-ref" ]] ||
  fail "replacement ref mutated the preflight artifact destination"
if MOCK_REMOTE_URL=https://github.com/steipete/crawlkit.git \
  bash "$ROOT/scripts/preflight-crawlctl-release.sh" \
    v0.14.3 "$WORK_DIR/preflight-wrong-origin" >/dev/null 2>&1; then
  fail "preflight accepted a non-official origin"
fi
[[ ! -e "$WORK_DIR/preflight-wrong-origin" ]] ||
  fail "non-official origin mutated the preflight artifact destination"
if MOCK_LOCAL_HEAD="$MOCK_SIDE_COMMIT" bash "$ROOT/scripts/preflight-crawlctl-release.sh" \
  v0.14.3 "$WORK_DIR/preflight-side-commit" >/dev/null 2>&1; then
  fail "preflight accepted a checkout outside protected main"
fi
[[ ! -e "$WORK_DIR/preflight-side-commit" ]] ||
  fail "side-commit preflight mutated the artifact destination"
preflight_commit=$(bash "$ROOT/scripts/preflight-crawlctl-release.sh" \
  v0.14.3 "$WORK_DIR/preflight")
[[ "$preflight_commit" == "$MOCK_COMMIT" ]] || fail "preflight reported the wrong commit"
for arch in arm64 x86_64; do
  archive="$WORK_DIR/preflight/crawlctl-v0.14.3-macos-${arch}.tar.gz"
  [[ -f "$archive" && -f "$archive.sha256" ]] || fail "missing preflight $arch artifact"
done
[[ "$(find "$WORK_DIR/preflight" -maxdepth 1 -type f | wc -l | tr -d ' ')" == 4 ]] ||
  fail "unexpected preflight artifact inventory"
grep -F 'codesign-run --with-package-secrets --' "$MOCK_HELPER_LOG" >/dev/null ||
  fail "package producer did not use the managed release helper"
awk '
  $0 == "cleanup" { cleaned = 1; next }
  $0 == "candidate" && !cleaned { exit 1 }
' "$MOCK_RELEASE_EVENT_LOG" || fail "candidate executed before managed keychain cleanup"
grep -F 'candidate' "$MOCK_RELEASE_EVENT_LOG" >/dev/null || fail "candidate probe was not instrumented"

if MOCK_BUILDINFO_REVISION="$MOCK_SIDE_COMMIT" \
  bash "$ROOT/scripts/package-crawlctl-release.sh" v0.14.0 "$WORK_DIR/wrong-build-revision" \
    >/dev/null 2>&1; then
  fail "package accepted a binary built from the wrong revision"
fi
[[ ! -e "$WORK_DIR/wrong-build-revision" ]] ||
  fail "wrong build revision mutated the artifact destination"

if MOCK_BUILDINFO_MODIFIED=true \
  bash "$ROOT/scripts/package-crawlctl-release.sh" v0.14.0 "$WORK_DIR/modified-build" \
    >/dev/null 2>&1; then
  fail "package accepted a binary with vcs.modified=true"
fi
[[ ! -e "$WORK_DIR/modified-build" ]] ||
  fail "modified build mutated the artifact destination"

if MOCK_BUILDINFO_PATH=github.com/openclaw/crawlkit/cmd/other \
  bash "$ROOT/scripts/package-crawlctl-release.sh" v0.14.0 "$WORK_DIR/wrong-package-path" \
    >/dev/null 2>&1; then
  fail "package accepted another main package built from the release commit"
fi
[[ ! -e "$WORK_DIR/wrong-package-path" ]] ||
  fail "wrong Go package path mutated the artifact destination"

promotion_failure="$(cd "$WORK_DIR" && pwd)/promotion-failure"
promotion_counter="$WORK_DIR/promotion-counter"
if MOCK_FAIL_PROMOTION_DIR="$promotion_failure" MOCK_PROMOTION_COUNTER="$promotion_counter" \
  bash "$ROOT/scripts/package-crawlctl-release.sh" v0.14.0 "$promotion_failure" \
    >/dev/null 2>&1; then
  fail "injected failure after the first artifact promotion was accepted"
fi
[[ ! -e "$promotion_failure" ]] || fail "failed promotion left partial official artifacts"
bash "$ROOT/scripts/package-crawlctl-release.sh" v0.14.0 "$promotion_failure" >/dev/null
[[ "$(find "$promotion_failure" -maxdepth 1 -type f | wc -l | tr -d ' ')" == 4 ]] ||
  fail "release packaging was not rerunnable after promotion rollback"

grep -F 'notarytool submit' "$MOCK_XCRUN_LOG" >/dev/null || fail "notarytool was not invoked"
grep -F -- '-c -k --keepParent' "$MOCK_DITTO_LOG" >/dev/null ||
  fail "notarization submission ZIP was not created through the mocked macOS contract"
grep -F -- '--no-s3-acceleration --wait --output-format json' "$MOCK_XCRUN_LOG" >/dev/null ||
  fail "notarytool did not use the required synchronous upload contract"
grep -F -- '--check-notarization' "$MOCK_CODESIGN_LOG" >/dev/null ||
  fail "online notarization tickets were not checked"
grep -F -- '-R=notarized' "$MOCK_CODESIGN_LOG" >/dev/null ||
  fail "online notarization checks did not require a notarized binary"
grep -F -- '-d -r-' "$MOCK_CODESIGN_LOG" >/dev/null ||
  fail "embedded designated requirements were not inspected"

MOCK_GH_ASSET_DIR="$WORK_DIR/gh-assets"
mkdir -p "$MOCK_GH_ASSET_DIR"
cp "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" "$MOCK_GH_ASSET_DIR/1"
cp "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz.sha256" "$MOCK_GH_ASSET_DIR/2"
cp "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-x86_64.tar.gz" "$MOCK_GH_ASSET_DIR/3"
cp "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-x86_64.tar.gz.sha256" "$MOCK_GH_ASSET_DIR/4"
MOCK_GH_RELEASES_JSON="$WORK_DIR/releases.json"
MOCK_GH_ASSETS_JSON="$WORK_DIR/assets.json"
cat > "$MOCK_GH_RELEASES_JSON" <<'EOF'
[{"id":42,"tag_name":"v0.13.4","draft":true}]
EOF
cat > "$MOCK_GH_ASSETS_JSON" <<'EOF'
[
  {"name":"crawlctl-v0.13.4-macos-arm64.tar.gz","url":"https://api.github.com/repos/openclaw/crawlkit/releases/assets/1"},
  {"name":"crawlctl-v0.13.4-macos-arm64.tar.gz.sha256","url":"https://api.github.com/repos/openclaw/crawlkit/releases/assets/2"},
  {"name":"crawlctl-v0.13.4-macos-x86_64.tar.gz","url":"https://api.github.com/repos/openclaw/crawlkit/releases/assets/3"},
  {"name":"crawlctl-v0.13.4-macos-x86_64.tar.gz.sha256","url":"https://api.github.com/repos/openclaw/crawlkit/releases/assets/4"}
]
EOF
export MOCK_GH_ASSET_DIR MOCK_GH_RELEASES_JSON MOCK_GH_ASSETS_JSON
api_download="$WORK_DIR/api-download"
GITHUB_REPOSITORY=openclaw/crawlkit GH_TOKEN=test \
  bash "$ROOT/scripts/download-crawlctl-release-assets.sh" v0.13.4 arm64 true "$api_download"
cmp "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" \
  "$api_download/crawlctl-v0.13.4-macos-arm64.tar.gz"
cmp "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz.sha256" \
  "$api_download/crawlctl-v0.13.4-macos-arm64.tar.gz.sha256"
if GITHUB_REPOSITORY=openclaw/crawlkit GH_TOKEN=test \
  bash "$ROOT/scripts/download-crawlctl-release-assets.sh" v0.13.4 arm64 false "$WORK_DIR/wrong-draft" \
    >/dev/null 2>&1; then
  fail "draft release matched published-release lookup"
fi

if MOCK_CODESIGN_AUTHORITY='Developer ID Application: Peter Steinberger (Y5PE65HELJ)' \
  bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.13.4 "$MOCK_COMMIT" \
    "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" >/dev/null 2>&1; then
  fail "personal signature was accepted"
fi
if MOCK_NOTARY_TICKET=missing \
  bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.13.4 "$MOCK_COMMIT" \
    "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" >/dev/null 2>&1; then
  fail "verifier accepted a missing notarization ticket"
fi
if MOCK_DESIGNATED_REQUIREMENT="$WRONG_REQUIREMENT" \
  bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.13.4 "$MOCK_COMMIT" \
    "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" >/dev/null 2>&1; then
  fail "verifier accepted a mismatched embedded designated requirement"
fi
if MOCK_LIPO_ARCHS='arm64 x86_64' \
  bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.13.4 "$MOCK_COMMIT" \
    "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" >/dev/null 2>&1; then
  fail "verifier accepted a universal binary in an architecture-specific asset"
fi
if MOCK_BUILDINFO_REVISION="$MOCK_SIDE_COMMIT" \
  bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.13.4 "$MOCK_COMMIT" \
    "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" >/dev/null 2>&1; then
  fail "verifier accepted a binary built from the wrong revision"
fi
if MOCK_BUILDINFO_MODIFIED=true \
  bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.13.4 "$MOCK_COMMIT" \
    "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" >/dev/null 2>&1; then
  fail "verifier accepted a binary with vcs.modified=true"
fi
if MOCK_BUILDINFO_PATH=github.com/openclaw/crawlkit/cmd/other \
  bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.13.4 "$MOCK_COMMIT" \
    "$WORK_DIR/v0.13.4/crawlctl-v0.13.4-macos-arm64.tar.gz" >/dev/null 2>&1; then
  fail "verifier accepted another main package built from the release commit"
fi
INSTALL_DIR="$WORK_DIR/install"
for version in v0.13.4 v0.14.0; do
  GH_TOKEN=caller-token GITHUB_TOKEN=caller-token MOCK_ASSET_DIR="$WORK_DIR/$version" \
    CRAWLCTL_DOWNLOAD_BASE_URL=https://example.invalid/releases/download/"$version" \
    bash "$ROOT/scripts/install-crawlctl.sh" "$version" "$INSTALL_DIR" >/dev/null
  [[ "$("$INSTALL_DIR/crawlctl" --version)" == "${version#v}" ]] || fail "installed version mismatch"
  if [[ "$version" == v0.13.4 ]]; then
    first_hash=$(shasum -a 256 "$INSTALL_DIR/crawlctl" | awk '{print $1}')
  fi
done
second_hash=$(shasum -a 256 "$INSTALL_DIR/crawlctl" | awk '{print $1}')
[[ "$first_hash" != "$second_hash" ]] || fail "update did not replace the executable"

installed_hash=$second_hash
if MOCK_NOTARY_TICKET=missing MOCK_ASSET_DIR="$WORK_DIR/v0.14.0" \
  CRAWLCTL_DOWNLOAD_BASE_URL=https://example.invalid/releases/download/v0.14.0 \
  bash "$ROOT/scripts/install-crawlctl.sh" v0.14.0 "$INSTALL_DIR" >/dev/null 2>&1; then
  fail "installer accepted a missing notarization ticket"
fi
[[ "$(shasum -a 256 "$INSTALL_DIR/crawlctl" | awk '{print $1}')" == "$installed_hash" ]] ||
  fail "missing-ticket install changed the destination"

if MOCK_DESIGNATED_REQUIREMENT="$WRONG_REQUIREMENT" \
  MOCK_ASSET_DIR="$WORK_DIR/v0.14.0" \
  CRAWLCTL_DOWNLOAD_BASE_URL=https://example.invalid/releases/download/v0.14.0 \
  bash "$ROOT/scripts/install-crawlctl.sh" v0.14.0 "$INSTALL_DIR" >/dev/null 2>&1; then
  fail "installer accepted a mismatched embedded designated requirement"
fi
[[ "$(shasum -a 256 "$INSTALL_DIR/crawlctl" | awk '{print $1}')" == "$installed_hash" ]] ||
  fail "wrong-designated-requirement install changed the destination"

if MOCK_LIPO_ARCHS='arm64 x86_64' MOCK_ASSET_DIR="$WORK_DIR/v0.14.0" \
  CRAWLCTL_DOWNLOAD_BASE_URL=https://example.invalid/releases/download/v0.14.0 \
  bash "$ROOT/scripts/install-crawlctl.sh" v0.14.0 "$INSTALL_DIR" >/dev/null 2>&1; then
  fail "installer accepted a universal binary in an architecture-specific asset"
fi
[[ "$(shasum -a 256 "$INSTALL_DIR/crawlctl" | awk '{print $1}')" == "$installed_hash" ]] ||
  fail "universal-binary install changed the destination"

if MOCK_CODESIGN_AUTHORITY='Developer ID Application: Peter Steinberger (Y5PE65HELJ)' \
  MOCK_ASSET_DIR="$WORK_DIR/v0.14.0" \
  CRAWLCTL_DOWNLOAD_BASE_URL=https://example.invalid/releases/download/v0.14.0 \
  bash "$ROOT/scripts/install-crawlctl.sh" v0.14.0 "$INSTALL_DIR" >/dev/null 2>&1; then
  fail "installer accepted the wrong signing identity"
fi
[[ "$(shasum -a 256 "$INSTALL_DIR/crawlctl" | awk '{print $1}')" == "$installed_hash" ]] ||
  fail "wrong-identity install changed the destination"

BAD_ASSETS="$WORK_DIR/bad-assets"
BAD_STAGE="$WORK_DIR/bad-stage"
mkdir -p "$BAD_ASSETS" "$BAD_STAGE"
cp "$INSTALL_DIR/crawlctl" "$BAD_STAGE/crawlctl"
echo unexpected > "$BAD_STAGE/unexpected"
bad_asset=crawlctl-v0.14.1-macos-arm64.tar.gz
tar -czf "$BAD_ASSETS/$bad_asset" -C "$BAD_STAGE" crawlctl unexpected
(
  cd "$BAD_ASSETS"
  shasum -a 256 "$bad_asset" > "$bad_asset.sha256"
)
if MOCK_ASSET_DIR="$BAD_ASSETS" \
  CRAWLCTL_DOWNLOAD_BASE_URL=https://example.invalid/releases/download/v0.14.1 \
  bash "$ROOT/scripts/install-crawlctl.sh" v0.14.1 "$INSTALL_DIR" >/dev/null 2>&1; then
  fail "archive with extra entries was accepted"
fi
[[ "$("$INSTALL_DIR/crawlctl" --version)" == 0.14.0 ]] || fail "failed update changed the installed executable"
if bash "$ROOT/scripts/verify-crawlctl-release.sh" v0.14.1 "$MOCK_COMMIT" \
  "$BAD_ASSETS/$bad_asset" >/dev/null 2>&1; then
  fail "verifier accepted an archive with extra entries"
fi

grep -F -- '--identifier org.openclaw.crawlctl' "$MOCK_CODESIGN_LOG" >/dev/null || fail "missing fixed identifier"
grep -F "$EXPECTED_TEAM_ID" "$MOCK_CODESIGN_LOG" >/dev/null || fail "missing fixed Team ID requirement"

echo "crawlctl release script tests passed"
