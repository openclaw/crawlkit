# Publishing Crawlkit

Go modules are published from signed git tags. GitHub releases are built by the
fleet `release-go-cli` workflow and contain Developer ID-signed, notarized
`crawlctl` binaries for macOS plus static Linux binaries.

## Release assets

Starting with v0.14.4, crawlkit uses the fleet archive convention. The archive
prefix is the repository name because that is the shared pipeline default; the
executable inside every archive remains `crawlctl`.

| Platform | Asset |
| --- | --- |
| macOS Apple Silicon | `crawlkit_<version>_darwin_arm64.tar.gz` |
| macOS Intel | `crawlkit_<version>_darwin_amd64.tar.gz` |
| Linux arm64 | `crawlkit_<version>_linux_arm64.tar.gz` |
| Linux amd64 | `crawlkit_<version>_linux_amd64.tar.gz` |
| All platforms | `checksums.txt` |

This deliberately replaces the v0.14.3 names. In particular,
`crawlctl-v0.14.3-macos-arm64.tar.gz` became
`crawlkit_0.14.4_darwin_arm64.tar.gz`, and
`crawlctl-v0.14.3-macos-x86_64.tar.gz` became
`crawlkit_0.14.4_darwin_amd64.tar.gz`. The version inside an asset name no
longer has a `v` prefix, `macos` is now `darwin`, `x86_64` is now `amd64`, and
the four per-asset `.sha256` sidecars are replaced by one `checksums.txt` file.
Linux arm64 and amd64 archives are new in v0.14.4.

The fixed macOS code identifier remains `org.openclaw.crawlctl`, and the
required signing identity remains `Developer ID Application: OpenClaw
Foundation (FWJYW4S8P8)`. The workflow signs each native Mach-O with the
hardened runtime, submits it for notarization, and independently verifies both
architectures before publication. There is no universal macOS archive, nFPM
package, or Homebrew handoff.

## Release checklist

1. Pull the protected default branch and confirm the checkout is clean.
2. Add the release notes to the versioned Unreleased section and run:

   ```bash
   make check
   actionlint
   ```

3. Merge the release-preparation PR, then date its changelog heading.
4. Create an SSH-signed annotated tag from the exact protected `main` commit
   and push it. The signer must match `.github/release-allowed-signers`.
5. Dispatch the unified workflow from the current protected `main` head:

   ```bash
   gh workflow run release-unified.yml --repo openclaw/crawlkit -f version=X.Y.Z
   ```

   Local release publishing is intentionally disabled. `make release`, the
   compatibility alias `make release-artifacts`, and
   `scripts/package-crawlctl-release.sh` only print the workflow command and
   fail.

6. Watch the workflow through publication. It freezes the signed tag, builds
   all four native archives, signs and notarizes the two macOS binaries,
   verifies the immutable draft independently on Apple Silicon and Intel, and
   publishes only the byte-for-byte verified assets.
7. Download every asset and `checksums.txt`, validate every checksum, inspect
   both Linux binaries, and verify a downloaded macOS binary with:

   ```bash
   codesign --verify --strict --check-notarization -R=notarized ./crawlctl
   ```

   `make verify-release VERSION=X.Y.Z` performs the repository's local
   diagnostics when the five release assets have already been downloaded into
   `dist/`.
8. Prime and verify module proxy visibility:

   ```bash
   GOPROXY=https://proxy.golang.org GONOSUMDB= go list -m github.com/openclaw/crawlkit@vX.Y.Z
   GOPROXY=https://proxy.golang.org go list -m github.com/openclaw/crawlkit@vX.Y.Z
   ```

9. Merge the workflow's closeout PR, or otherwise add the next Unreleased
   changelog section.

Use a patch tag only for narrow fixes on the existing API. Use a minor tag for
broad crawler infrastructure changes. If the module reaches v2, Go requires
the module path to become `github.com/openclaw/crawlkit/v2`.
