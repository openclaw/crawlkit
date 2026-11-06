# Distribution with Crawlkit

`sessionowner` is an ordinary Go package in the existing
`github.com/openclaw/crawlkit` module. Its import path remains
`github.com/openclaw/crawlkit/sessionowner`; it uses the root module's version and
normal Crawlkit release process. No nested module, separate component version or
tag, standalone source envelope, component selector or shared-publisher extension
is required.

The normal [Crawlkit publishing workflow](../docs/publishing.md) is unchanged.
The private Node binding stays source under `bindings/node/`, with its current
API, schema references, `private: true` and `1.0.0-candidate.0` metadata retained.
That metadata is not a separate publication commitment. No npm package, binary,
installation or runtime enrollment is introduced by this package integration.

## Validation and adoption

Removing the nested module boundary makes the existing root `go test ./...` and
`make check` entrypoints include this package. Its Go implementation, tests,
public import path, wire contracts and Node/schema bytes are otherwise unchanged.
The root `go.mod`, `go.sum`, Go 1.27.3 toolchain selection and dependencies remain
unchanged; no workspace or local replacement substitutes for ordinary module
ownership.

Compilation and tests in the root-module context must pass with the required
current toolchain, locally or through native CI, before landing. Static module
ownership and carried-forward standalone test receipts do not prove that gate.
Older Go, automatic toolchain downloads, environment workarounds and replaying
old matrices are not substitutes. Required final-candidate review also remains
separate from the local integration commit.

Crawlkit package availability does not qualify a native storage backend,
provider-specific adapter, browser/SDK integration or consumer rollout. Native
owners still need exact source/version pins and applicable provider, storage,
security and installed-identity acceptance. Existing Bird/Birdcrawl, X, Main,
remote-reader, SQL and starter holds remain in force; no consumer pin or runtime
was changed. Code review and landing do not authorize a package release, tag,
workflow dispatch, installation, activation or provider access.

## Retained implementation history

The accepted offline candidate was preserved in commit
`2eafe56786e4b6319447188adba985f65477b178` and imported onto the chosen Crawlkit
main baseline by `1286456f8d091ea633858158d34047e262b1ddca`. Those commits and their
frozen evidence remain retained.

The later independent-distribution adapter and caller commits
`174254ffc7dbcc6a964192c24820952fb3785de1` and
`27a4d90a2170165498b86333ee85f2b54da50a35` record a superseded design. The owner's
2026-11-06 decision selected normal Crawlkit distribution instead. A subsequent
ordinary commit removes only that task-owned plumbing; history and historical
stage receipts are not rewritten. The active [README](README.md) and
[design](DESIGN.md) describe the current package and its behavioral boundaries.
