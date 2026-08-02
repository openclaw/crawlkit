# Publishing Crawlkit

`crawlkit` is a Go library with the optional `crawlctl` CLI. Releases use the
fleet `release-go-cli` workflow from `openclaw/release-workflows`. The workflow
owns tag creation, Developer ID signing, notarization, independent artifact
verification, and GitHub Release publication. Do not create release tags or
handle signing credentials locally.

## Release assets

The archive prefix is the repository name; the executable inside every archive
is `crawlctl`.

| Platform | Asset |
| --- | --- |
| macOS Apple Silicon | `crawlkit_<version>_darwin_arm64.tar.gz` |
| macOS Intel | `crawlkit_<version>_darwin_amd64.tar.gz` |
| Linux arm64 | `crawlkit_<version>_linux_arm64.tar.gz` |
| Linux amd64 | `crawlkit_<version>_linux_amd64.tar.gz` |
| All platforms | `checksums.txt` |

The fixed macOS code identifier is `org.openclaw.crawlctl`, and the required
signing identity is `Developer ID Application: OpenClaw Foundation
(FWJYW4S8P8)`. The workflow signs each native Mach-O with the hardened runtime,
submits it for notarization, and independently verifies both architectures
before publication. crawlkit has no universal macOS archive, nFPM package, or
Homebrew handoff.

## Release checklist

1. Prepare a release PR from the current protected `main` head. Date the
   versioned changelog section and run:

   ```bash
   make check
   actionlint
   ```

2. Merge the release-preparation PR and confirm its exact merge commit is green.
3. Dispatch the unified workflow from the current protected `main` head:

   ```bash
   gh workflow run release-unified.yml --repo openclaw/crawlkit -f version=X.Y.Z
   ```

4. Watch the exact workflow run through publication. It creates or reuses an
   immutable annotated version tag, builds all four native archives, signs and
   notarizes the two macOS binaries, verifies the immutable draft independently
   on Apple Silicon and Intel, and publishes only the verified bytes.
5. Confirm the GitHub Release notes match the dated changelog section. Download
   every release asset, verify `checksums.txt`, inspect both Linux binaries, and
   verify a downloaded macOS binary with:

   ```bash
   codesign --verify --strict --check-notarization -R=notarized ./crawlctl
   ```

6. Prime and verify module proxy visibility:

   ```bash
   GOPROXY=https://proxy.golang.org GONOSUMDB= go list -m github.com/openclaw/crawlkit@vX.Y.Z
   GOPROXY=https://proxy.golang.org go list -m github.com/openclaw/crawlkit@vX.Y.Z
   ```

7. Merge the workflow's closeout PR, or otherwise add the next patch-version
   Unreleased changelog section.

Use a patch version for narrow fixes on the existing API. Use a minor version
for broad crawler infrastructure changes. If the module reaches v2, Go requires
the module path to become `github.com/openclaw/crawlkit/v2`.
