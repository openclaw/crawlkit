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
6. Pull the final protected `main` commit into a clean checkout.

```bash
git switch main
git pull --ff-only
git status --short --branch
```

7. Run a complete notarization preflight from that exact release candidate with
   the approved private signing environment. This does not
   inspect or create a tag. It produces disposable candidates, restores the
   managed keychain, then runs the credential-free online-ticket and build
   provenance verifier:

```bash
preflight_dir=$(mktemp -d /private/tmp/crawlkit-v0.14.3-preflight.XXXXXX)
scripts/preflight-crawlctl-release.sh v0.14.3 "$preflight_dir"
```

8. Tag the next semver release from `main`:

```bash
git tag -s v0.14.3
git push origin main
git push origin v0.14.3
```

9. From the clean checkout whose `HEAD` exactly matches the signed release tag,
build the signed macOS artifacts. The package entrypoint fetches the remote tag
and protected `main` head into temporary refs, verifies the annotated tag against
the repository-pinned SSH signer principal and key, requires the signed tag
object's embedded name to equal the requested version, and requires the tag,
remote branch, and local checkout to resolve to one commit. A signed side-commit
or renamed tag is not releasable. It then uses the shared secret-safe release
keychain helper and fails closed if any provenance or signing check differs:

```bash
make release-artifacts VERSION=v0.14.3
release_commit=$(scripts/verify-crawlctl-release-provenance.sh v0.14.3)
scripts/verify-crawlctl-release.sh v0.14.3 "$release_commit" \
  dist/crawlctl-v0.14.3-macos-arm64.tar.gz \
  dist/crawlctl-v0.14.3-macos-x86_64.tar.gz
```

The fixed code identifier is `org.openclaw.crawlctl`. The required signing
identity is `Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)`; the
release scripts reject every other certificate or Team ID. Never rebuild,
strip, patch, or re-sign an artifact after this step. The tracked
`.mac-release.env.example` contains only the public signing identity and
managed, passwordless-keychain policy. Keep `.mac-release.env` ignored and
inject the runtime signing-keychain routing and
`NOTARYTOOL_KEYCHAIN_PROFILE` through the approved private environment; never
commit their values or locators. Ordinary `go build` and `go install` remain
unsigned/local workflows and do not require signing or notarization
credentials.

For each architecture, the managed helper builds one thin executable with Go
VCS metadata, signs it with the hardened runtime, creates an ephemeral ZIP, and
runs `xcrun notarytool submit` with `--no-s3-acceleration --wait`. It requires
an accepted response, the exact embedded designated requirement, and an online
notarization-ticket lookup before creating the existing tarball and checksum.
The helper finishes and restores the signing keychain state before the package
entrypoint invokes either candidate. The credential-free verifier uses
`go version -m` to require `vcs.revision` equal to the verified remote release
commit, `vcs.modified=false`, and the exact
`github.com/openclaw/crawlkit/cmd/crawlctl` main-package path; its version probe
runs with a minimal environment so caller tokens are absent. Final artifacts
move through a destination-side staging directory, and any promotion failure
rolls back the entire four-file set so the command is safely rerunnable.

The ZIP is only a submission carrier and is never a release asset. Raw Mach-O
executables and ZIP archives cannot carry a stapled ticket, so both release
verification and installation need network access to retrieve the ticket from
Apple. Do not use
`spctl --assess --type execute` as a raw-CLI gate: even valid Apple-notarized
standalone executables can exit with “the code is valid but does not seem to be
an app.”

10. Create the GitHub release as a draft, attach both archives and their
`.sha256` files, then run the `Release Assets` workflow manually from the
default branch with the tag to verify the uploaded draft assets. Publish only
after that check succeeds. A crawlkit release without both verified signed
macOS assets is incomplete. The workflow runs again on publication and uses its
own trusted revision to reject missing, wrongly-signed, checksum-invalid,
ticketless, universal, dirty, wrong-revision, or side-commit artifacts. At both
draft and publication gates it refetches the remote tag and protected branch,
rechecks the pinned signer and exact commit equality, then binds every binary's
Go build info to that commit. This protected-main verifier uses no notarization
credentials; it performs only public provenance, online-ticket, and
signature-policy checks.

Before publishing, run two independent clean-VM checks and record their results
separately:

- Gatekeeper: acquire the draft artifact through a path that naturally applies
  and preserves quarantine, then execute it on the clean VM and prove there is
  no security alert. A `curl`/`scp`/`tar` transfer and raw-CLI `spctl` assessment
  are supporting evidence only and must not be called a fresh-download
  Gatekeeper pass.
- Full Disk Access: grant the expected first protected-data consent once to the
  official stable-path binary, update that same path to a distinct later build
  with the identical designated requirement, and prove the same protected-data
  action succeeds without a second alert. Do not claim first-install prompt
  suppression.

11. Prime and verify module proxy visibility:

```bash
GOPROXY=https://proxy.golang.org GONOSUMDB= go list -m github.com/openclaw/crawlkit@v0.14.3
GOPROXY=https://proxy.golang.org go list -m github.com/openclaw/crawlkit@v0.14.3
```

12. Bump downstream apps to the new tag and commit their `go.mod`/`go.sum` updates:

```bash
go get github.com/openclaw/crawlkit@v0.14.3
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
