# Publishing Crawlkit

Go modules are published from git tags. GitHub releases additionally carry
Developer ID-signed `crawlctl` binaries for macOS. Users must install those
artifacts instead of rebuilding the production executable so macOS can preserve
its TCC/Full Disk Access identity across updates.

## Release checklist

1. Rebase `crawlkit` and every downstream app branch on each repo's `origin/main`.
2. Run the crawlkit gate:

```bash
GOWORK=off go mod tidy
git diff --exit-code -- go.mod go.sum
GOWORK=off go vet ./...
GOWORK=off go test -count=1 ./...
```

3. Update docs and changelogs in `crawlkit` plus every downstream app branch
   that consumes the release.
4. Test downstream apps against the local checkout through a temporary Go workspace.
5. Merge `crawlkit` to `main`.
6. Tag the next semver release from `main`:

```bash
git tag -s v0.13.4
git push origin main
git push origin v0.13.4
```

7. From the clean checkout whose `HEAD` exactly matches the signed release tag,
build the signed macOS artifacts. This uses the shared secret-safe release
keychain helper and fails closed if the tag, checkout, or Developer ID signing
identity is invalid:

```bash
make release-artifacts VERSION=v0.13.4
scripts/verify-crawlctl-release.sh v0.13.4 \
  dist/crawlctl-v0.13.4-macos-arm64.tar.gz \
  dist/crawlctl-v0.13.4-macos-x86_64.tar.gz
```

The fixed code identifier is `org.openclaw.crawlctl`. The required signing
identity is `Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)`; the
release scripts reject every other certificate or Team ID. Never rebuild,
strip, patch, or re-sign an artifact after this step. Keychain and
credential-routing settings belong in the maintainer's ignored
`.mac-release.env` or approved private environment, never in Git. Ordinary
`go build` and `go install` remain unsigned/local workflows and do not require
the release identity.

8. Create the GitHub release as a draft, attach both archives and their
`.sha256` files, then run the `Release Assets` workflow manually from the
default branch with the tag to verify the uploaded draft assets. Publish only
after that check succeeds. A crawlkit release without both verified signed
macOS assets is incomplete. The workflow runs again on publication and uses its
own trusted revision to reject missing, wrongly-signed, or checksum-invalid
artifacts.

9. Prime and verify module proxy visibility:

```bash
GOPROXY=https://proxy.golang.org GONOSUMDB= go list -m github.com/openclaw/crawlkit@v0.13.4
GOPROXY=https://proxy.golang.org go list -m github.com/openclaw/crawlkit@v0.13.4
```

10. Bump downstream apps to the new tag and commit their `go.mod`/`go.sum` updates:

```bash
go get github.com/openclaw/crawlkit@v0.13.4
GOWORK=off go mod tidy
```

`pkg.go.dev` indexes public modules automatically after the tag is reachable.

Use a patch tag only for narrow bug fixes on the existing API. Use a minor tag
for broad crawler infrastructure changes. The module-path move needs a new tag
on `openclaw/crawlkit` before downstream apps can drop local `replace` lines.

## Versioning

Keep `v0.x.y` while the downstream crawler rewires are still settling. If the
module ever reaches `v2`, Go requires the module path to become:

```text
github.com/openclaw/crawlkit/v2
```
