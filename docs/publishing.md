# Publishing Crawlkit

`crawlkit` is a Go library with the optional `crawlctl` CLI. Releases use the
fleet `release-go-cli` workflow from `openclaw/release-workflows`. The workflow
owns tag creation, Developer ID signing, notarization, independent artifact
verification, and GitHub Release publication. Do not create release tags or
handle signing credentials locally.

v0.14.5 is an SSH-signed Go module tag without a GitHub Release or attached
artifacts. v0.14.4 remains the latest historical release with binary assets.
The unified pipeline applies to future releases.

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

1. Confirm the repository can access all five required Actions secrets:
   `MACOS_SIGNING_P12`, `MACOS_SIGNING_P12_PASSWORD`, `ASC_KEY_ID`,
   `ASC_ISSUER_ID`, and `ASC_PRIVATE_KEY_P8`. The shared workflow validates
   them before creating a tag.
2. Prepare a release PR from the current protected `main` head. Date the
   versioned changelog section and run:

   ```bash
   make check
   actionlint
   ```

3. Merge the release-preparation PR and confirm its exact merge commit is green.
4. Dispatch the unified workflow from the current protected `main` head:

   ```bash
   gh workflow run release-unified.yml --repo openclaw/crawlkit -f version=X.Y.Z
   ```

5. Watch the exact workflow run through publication. It creates or reuses an
   immutable annotated version tag, builds all four native archives, signs and
   notarizes the two macOS binaries, verifies the immutable draft independently
   on Apple Silicon and Intel, and publishes only the verified bytes.
6. Confirm the GitHub Release notes match the dated changelog section. Download
   every release asset, verify `checksums.txt`, inspect both Linux binaries, and
   verify a downloaded macOS binary with:

   ```bash
   codesign --verify --strict --check-notarization -R=notarized ./crawlctl
   ```

7. Prime and verify module proxy visibility:

   ```bash
   GOPROXY=https://proxy.golang.org GONOSUMDB= go list -m github.com/openclaw/crawlkit@vX.Y.Z
   GOPROXY=https://proxy.golang.org go list -m github.com/openclaw/crawlkit@vX.Y.Z
   ```

8. Merge the workflow's closeout PR, or otherwise add the next patch-version
   Unreleased changelog section.

Never delete or force-update a release tag. If a run fails after tag creation,
fix the blocker and rerun the same version; the shared workflow requires the
exact annotated tag object and target observed during validation.

Use a patch version for narrow fixes on the existing API. Use a minor version
for broad crawler infrastructure changes. If the module reaches v2, Go requires
the module path to become `github.com/openclaw/crawlkit/v2`.
