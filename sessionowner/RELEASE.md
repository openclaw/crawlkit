# Independent distribution gate

This nested module and private Node binding are locally integrated, not release
ready. The accepted 111-file offline candidate was preserved in local commit
`2eafe56786e4b6319447188adba985f65477b178` on
`feat/provider-session-core-v1-20261105`, then imported byte-for-byte by commit
`1286456f8d091ea633858158d34047e262b1ddca` on
`feat/sessionowner-integration-20261106`, based on observed protected main
`2c65e66e252688e576ad8090ec44ce9a89dfaa4a`. This status update is separate from that
unchanged import. No real-component release artifact, push, tag, binary, npm
publication or installation was produced; adapter tests create only temporary
synthetic fixture packages.

The intended independent module tag is `sessionowner/v1.0.0`, subject to a fresh
availability check and separately admitted release engineering. No moving branch,
local replace, go.work, root Crawlkit upgrade or consumer pin change substitutes
for a reviewed immutable component.

The earlier integration reference `783a0a32494df01a47345be85fbb5c5881febdd7`
was inspected in Git object storage, not checked out. C1 intentionally branched
from `d688e291177a4fd7b1969866a314a40ef7345054`; the preservation branch retains
that root and the accepted candidate. The admitted local integration imported
only the component addition onto the newer main commit above, leaving its root
tree unchanged. It does not establish release or consumer qualification.

The existing release route is root-only. Inspection of the shared
`release-source.yml@v1` at `9d81ab3d2877f9900ef787e6dbcf4d9af96eb013` found no
supported namespaced primary tag with a separate module version, component
metadata scope, or explicit `make_latest=false`. A narrow shared extension has
separate owner authorization. Crawlkit caller wiring still awaits its frozen,
verified interface; no input names are guessed or root-tag fallback introduced.
A later reviewed opt-in component route must preserve root behavior, workflow
pins, root tags, Crawlctl build/install and its default update selector.
A namespaced GitHub release must explicitly use `make_latest=false`; verify both root `/releases/latest` and the Crawlctl selector
before and after. The newer upstream policy requires a qualified dry run and a
fleet-signed annotated tag on the exact release commit. Follow the admitted route;
never improvise local publication or change signing policy from this candidate.

Root validation with the required Go 1.27.3, workflow linting with actionlint, and
supported pinned ClawSweeper coverage of the final integration and release route
remain open. The byte-preserving local integration did not rerun Go/Node tests,
builds, release rehearsal or review. Subsequent adapter validation runs only the
focused synthetic Node tests below, not accepted core, race or ABI suites.

Delivery requires complete relevant conformance, independent review, supported
pinned ClawSweeper final-candidate coverage, immutable source/checksum/API/schema
manifests and exact Go proxy identity. Native owners separately pin and qualify
the component. Browser/SDK/actor proof, installed identity, sequential provider
acceptance and every starter dependent remain independent gates.

## Offline generated-asset adapter

[`release/package.mjs`](release/package.mjs) prepares one bounded `.tar.gz` for
the existing shared `RELEASE_ASSET_PATH` seam. It does not publish, sign, install,
execute the binding, access providers, or replace the shared release pipeline.
The output contains only:

- `source.tar.gz`: the exact committed `sessionowner/` tree and root license,
  under `source/`;
- `private-node.tgz`: private binding bytes and ABI schemas under `package/`;
- `manifest.json`: repository, exact HEAD, component Git tree, Go module/version,
  private binding version, ABI/schema hashes, source blob/file hashes and archive
  digests;
- `SHA256SUMS`: checksums of the two inner archives and manifest.

A clean checkout, full expected commit SHA, explicit module SemVer, canonical
Crawlkit origin and an unused output path outside the checkout are required.
The adapter rejects untracked or ignored component extras, nonregular entries,
unsafe paths, local Go overrides, known credential files/token patterns and
schema hash drift. Bounded USTAR verification reads without extracting and
rejects extra, duplicate, linked or altered archive entries. This admission is
not a replacement for mandatory security review or secret scanning.

The binding reads `../../schema/v1` relative to its existing entry point, so the
pack retains unchanged files at `package/bindings/node/` and
`package/schema/v1/`. Only the outer generated `package/package.json` rebases
exports into `bindings/node/` and omits scripts; the native metadata stays
unchanged inside. The pack remains private and keeps version
`1.0.0-candidate.0`. The separately supplied Go module version is recorded, not
silently copied into package metadata. The caller must validate reviewed release
metadata and tag availability before any separately authorized release.

The helper's CLI is `node sessionowner/release/package.mjs --version SEMVER
--commit FULL_SHA`, with an absolute `RELEASE_ASSET_PATH` supplied by the caller.
It uses Node 24, Git and the existing system `tar`, with no dependency download.
Its exported `verifyRelease` requires the expected checkout/commit/version, not
only the envelope's self-reported identity. No real component package or release
rehearsal has been executed during this adapter preparation.

Run only its offline fixture tests with:

```sh
node --test sessionowner/release/package.test.mjs
```

Those tests create isolated synthetic Git repositories, never import the actual
binding, and cover source custody, private layout, archive integrity, metadata,
unsafe paths, credential rejection and output preservation. Root/default release
behavior is unchanged; component caller integration and final joint review are
still pending.

Do not publish or activate this component as a way to resolve a held consumer,
shared-profile dependency or missing browser capability. No Bird enrollment or
root release is authorized by the existence of this namespace.
