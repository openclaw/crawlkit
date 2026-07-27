#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

fail() {
  echo "release contract test failed: $*" >&2
  exit 1
}

for script in install-crawlctl.sh package-crawlctl-release.sh \
  verify-crawlctl-release-provenance.sh verify-crawlctl-release.sh; do
  bash -n "$ROOT/scripts/$script"
done

[[ ! -e "$ROOT/.github/workflows/release-assets.yml" ]] ||
  fail "legacy Release Assets workflow still exists"
[[ ! -e "$ROOT/scripts/preflight-crawlctl-release.sh" ]] ||
  fail "legacy local release preflight still exists"
[[ ! -e "$ROOT/scripts/download-crawlctl-release-assets.sh" ]] ||
  fail "legacy draft-asset downloader still exists"

workflow="$ROOT/.github/workflows/release-unified.yml"
grep -F 'uses: openclaw/release-workflows/.github/workflows/release-go-cli.yml@v1' "$workflow" >/dev/null
grep -F 'checksum-filename: checksums.txt' "$workflow" >/dev/null
grep -F 'nfpm: disabled' "$workflow" >/dev/null
grep -F 'stable-identifier: org.openclaw.crawlctl' "$workflow" >/dev/null
grep -F 'require-signed-tag: true' "$workflow" >/dev/null
grep -F 'darwin-universal: disabled' "$workflow" >/dev/null
if grep -Eq 'homebrew-|TAP_TOKEN|archive-(name|template)|sidecar' "$workflow"; then
  fail "unified workflow contains an unsupported release customization"
fi

config="$ROOT/.goreleaser.yaml"
grep -F 'project_name: crawlkit' "$config" >/dev/null
grep -F 'binary: crawlctl' "$config" >/dev/null
grep -F 'name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"' "$config" >/dev/null
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  grep -F -- "- $target" "$config" >/dev/null || fail "missing GoReleaser target $target"
done
if grep -Eq 'windows_|nfpms:|universal' "$config"; then
  fail "GoReleaser config contains an unexpected release target or package"
fi

grep -Fx \
  'steipete@gmail.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA6rFpd7CodTF6fy60LZTriTeiGAJ7haIBWD4hrdxmDB' \
  "$ROOT/.github/release-allowed-signers" >/dev/null
grep -Fx \
  'steipete@gmail.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAII9XsaCcr8TInPnHcuTVfvXXcsoUFrOE7menfbEIHFW9' \
  "$ROOT/.github/release-allowed-signers" >/dev/null
[[ "$(wc -l < "$ROOT/.github/release-allowed-signers" | tr -d ' ')" == 2 ]] ||
  fail "release signer policy must contain exactly the historical and current reviewed signers"

release_output=$(bash "$ROOT/scripts/package-crawlctl-release.sh" v0.14.4 2>&1) &&
  fail "local package script unexpectedly succeeded"
grep -Fx 'gh workflow run release-unified.yml --repo openclaw/crawlkit -f version=0.14.4' \
  <<<"$release_output" >/dev/null || fail "local package refusal omitted the dispatch command"

grep -F 'crawlkit_${release_version}_darwin_${arch}.tar.gz' \
  "$ROOT/scripts/install-crawlctl.sh" >/dev/null
grep -F 'checksums.txt' "$ROOT/scripts/install-crawlctl.sh" >/dev/null
if grep -Eq 'crawlctl-\$\{VERSION\}-macos-|\.tar\.gz\.sha256' "$ROOT/scripts"/*.sh; then
  fail "release scripts still reference the retired asset contract"
fi

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/crawlctl-release-contract.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOWORK=off \
    go build -trimpath -ldflags '-s -w -X main.version=0.14.4' \
    -o "$WORK_DIR/crawlctl-linux-$arch" "$ROOT/cmd/crawlctl"
done
if [[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]]; then
  [[ "$($WORK_DIR/crawlctl-linux-amd64 --version)" == 0.14.4 ]] ||
    fail "Linux amd64 version probe failed"
elif command -v file >/dev/null; then
  file "$WORK_DIR/crawlctl-linux-amd64" | grep -F 'ELF 64-bit' >/dev/null ||
    fail "Linux amd64 build is not an ELF executable"
fi

echo "crawlctl release contract tests passed"
