# Publishing Crawlkit

Go modules are published from git tags. There is no separate registry upload.

## Release checklist

1. Rebase `crawlkit` and every downstream app branch on each repo's `origin/main`.
2. Run the crawlkit gate:

```bash
go mod tidy
git diff --exit-code -- go.mod go.sum
go vet ./...
go test ./...
```

3. Update docs and changelogs in `crawlkit` plus every downstream app branch
   that consumes the release.
4. Test downstream apps against the local checkout through a temporary Go workspace.
5. Merge `crawlkit` to `main`.
6. Tag the next semver release from `main`:

```bash
git tag -s v0.5.0
git push origin main
git push origin v0.5.0
```

7. Prime and verify module proxy visibility:

```bash
GOPROXY=https://proxy.golang.org go list -m github.com/openclaw/crawlkit@v0.5.0
go list -m github.com/openclaw/crawlkit@v0.5.0
```

8. Bump downstream apps to the new tag and commit their `go.mod`/`go.sum` updates:

```bash
go get github.com/openclaw/crawlkit@v0.5.0
go mod tidy
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
