# Publishing Crawlkit

`crawlkit` is a Go library with the optional `crawlctl` CLI. Releases use the
fleet `release-go-cli` workflow from `openclaw/release-workflows`. The workflow
owns tag creation, Developer ID signing, notarization, independent artifact
verification, and GitHub Release publication. Do not create release tags or
handle signing credentials locally.

v0.14.7 is published with signed CLI assets and is available from the Go module
proxy. A second dispatch for that version rebuilt the payload and correctly
failed its comparison with the existing public release; it left a separate
unpublished draft. See the [incident evidence](release-31838411168.md).

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
2. Choose an unused version and prepare a release PR from the current protected
   `main` head. For the next release, use **v0.14.8**, not v0.14.7. It will carry
   the post-v0.14.7 release-check HTTP timeout fix, dependency/toolchain updates,
   and dispatch guard. Date the versioned changelog section and run (Node.js is
   required for the dispatch regression tests):

   ```bash
   make check
   actionlint
   ```

3. Merge the release-preparation PR and confirm its exact merge commit is green.
4. Dispatch the unified workflow from the current protected `main` head:

   ```bash
   gh workflow run release-unified.yml --repo openclaw/crawlkit -f version=X.Y.Z
   ```

5. Watch the exact workflow run through publication. A read-only preflight
   rejects existing tags and releases before the shared workflow runs. A
   repository-wide workflow lock spans preflight through publication, so queued
   dispatches (including `X.Y.Z` and `vX.Y.Z`) recheck state after the previous
   run finishes. The shared workflow creates an annotated version tag, builds all four native archives, signs and
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

Never delete or force-update a release tag or replace published assets. A Git
tag is already a public Go module version, even when GitHub publication fails.
Fresh dispatches require a new version once a tag exists; this also blocks
leftover drafts because the shared pipeline freezes the tag before drafting.
The release lookup additionally rejects a visible release whose tag is missing.
API failures other than an explicit 404 stop preflight.

After a failure, inspect the exact run, tag, release IDs, and retained artifacts
before taking action. Do not rerun all jobs or redispatch a published version:
rebuilding/re-signing does not reproduce the original verified archive bytes.
Any recovery of an unpublished draft must retain its original payload and
attestations, and requires a separately reviewed recovery decision. If those
bytes cannot be recovered, use a new patch version. Old workflow runs retain
their original workflow definition; this guard does not retrofit historical
reruns. Keep the shared workflow's final byte-binding checks intact.

Use a patch version for narrow fixes on the existing API. Use a minor version
for broad crawler infrastructure changes. If the module reaches v2, Go requires
the module path to become `github.com/openclaw/crawlkit/v2`.
