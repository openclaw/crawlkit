# Publishing Crawlkit

`crawlkit` is a Go library. Releases are signed git tags, which are the source
used by the Go module proxy. The repository publishes no release binaries, and
there is no release workflow to dispatch. Users who want the optional CLI
install it from source:

```bash
go install github.com/openclaw/crawlkit/cmd/crawlctl@latest
```

v0.14.4 was the last release with attached artifacts. Do not delete or modify
those historical releases or their assets.

## Release checklist

1. Pull the protected default branch and confirm the checkout is clean.
2. Add the release notes to the versioned Unreleased section and run:

   ```bash
   make check
   actionlint
   ```

3. Merge the release-preparation PR, then date its changelog heading.
4. Create an SSH-signed annotated tag from the exact protected `main` commit
   using the current signer recorded in `.github/release-allowed-signers`:

   ```bash
   git switch main
   git pull --ff-only
   test -z "$(git status --porcelain)"
   release_commit="$(git rev-parse HEAD)"
   test "$release_commit" = "$(git rev-parse origin/main)"
   git -c gpg.format=ssh tag -s -a vX.Y.Z "$release_commit" -m "crawlkit vX.Y.Z"
   git -c gpg.format=ssh -c gpg.ssh.allowedSignersFile=.github/release-allowed-signers verify-tag vX.Y.Z
   test "$(git rev-parse 'vX.Y.Z^{commit}')" = "$release_commit"
   git push origin vX.Y.Z
   ```

5. Prime and verify module proxy visibility:

   ```bash
   GOPROXY=https://proxy.golang.org GONOSUMDB= go list -m github.com/openclaw/crawlkit@vX.Y.Z
   GOPROXY=https://proxy.golang.org go list -m github.com/openclaw/crawlkit@vX.Y.Z
   ```

6. Add the next patch-version Unreleased changelog section and merge it.

Use a patch tag only for narrow fixes on the existing API. Use a minor tag for
broad crawler infrastructure changes. If the module reaches v2, Go requires
the module path to become `github.com/openclaw/crawlkit/v2`.
