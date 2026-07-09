#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION=${1:-}
REMOTE=origin
DEFAULT_BRANCH=main
ALLOWED_SIGNERS="$ROOT/.github/release-allowed-signers"

[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || {
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
}
[[ -s "$ALLOWED_SIGNERS" ]] || {
  echo "missing repository-pinned release signer policy" >&2
  exit 1
}
for tool in awk git; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done
remote_url=$(git -C "$ROOT" remote get-url "$REMOTE")
case "$remote_url" in
  https://github.com/openclaw/crawlkit | https://github.com/openclaw/crawlkit.git | \
    git@github.com:openclaw/crawlkit | git@github.com:openclaw/crawlkit.git | \
    ssh://git@github.com/openclaw/crawlkit | ssh://git@github.com/openclaw/crawlkit.git) ;;
  *)
    echo "release provenance requires the official openclaw/crawlkit origin" >&2
    exit 1
    ;;
esac
remote_default_ref=$(git -C "$ROOT" ls-remote --symref "$REMOTE" HEAD |
  awk '$1 == "ref:" && $3 == "HEAD" { print $2 }')
[[ "$remote_default_ref" == "refs/heads/$DEFAULT_BRANCH" ]] || {
  echo "official release origin default branch is not $DEFAULT_BRANCH" >&2
  exit 1
}

ref_namespace="refs/crawlctl-release-proof/$$-$RANDOM"
tag_ref="$ref_namespace/tag"
branch_ref="$ref_namespace/branch"
cleanup() {
  git -C "$ROOT" update-ref -d "$tag_ref" >/dev/null 2>&1 || true
  git -C "$ROOT" update-ref -d "$branch_ref" >/dev/null 2>&1 || true
}
trap cleanup EXIT

git -C "$ROOT" fetch --force --no-tags "$REMOTE" \
  "+refs/tags/$VERSION:$tag_ref" \
  "+refs/heads/$DEFAULT_BRANCH:$branch_ref" >/dev/null
[[ "$(git -C "$ROOT" cat-file -t "$tag_ref")" == tag ]] || {
  echo "remote release tag must be an annotated signed tag: $VERSION" >&2
  exit 1
}
tag_object=$(git -C "$ROOT" cat-file tag "$tag_ref")
tag_header=$(printf '%s\n' "$tag_object" | awk 'NF == 0 { exit } { print }')
embedded_tag=$(printf '%s\n' "$tag_header" | awk '/^tag / { print substr($0, 5) }')
[[ "$embedded_tag" == "$VERSION" ]] || {
  echo "remote signed tag object does not name the requested release: $VERSION" >&2
  exit 1
}
signature_begin=$(printf '%s\n' "$tag_object" | awk '/^-----BEGIN .*-----$/ { print }')
signature_end=$(printf '%s\n' "$tag_object" | awk '/^-----END .*-----$/ { print }')
[[ "$signature_begin" == "-----BEGIN SSH SIGNATURE-----" &&
  "$signature_end" == "-----END SSH SIGNATURE-----" ]] || {
  echo "remote release tag must contain exactly one SSH signature: $VERSION" >&2
  exit 1
}
git -C "$ROOT" \
  -c "gpg.ssh.allowedSignersFile=$ALLOWED_SIGNERS" \
  -c gpg.minTrustLevel=fully \
  verify-tag "$tag_ref" >/dev/null 2>&1 || {
  echo "remote release tag is not signed by a repository-pinned release signer: $VERSION" >&2
  exit 1
}

tag_commit=$(git -C "$ROOT" rev-parse "$tag_ref^{commit}")
branch_commit=$(git -C "$ROOT" rev-parse "$branch_ref^{commit}")
head_commit=$(git -C "$ROOT" rev-parse HEAD)
[[ "$tag_commit" == "$branch_commit" ]] || {
  echo "remote release tag does not target the protected $DEFAULT_BRANCH head: $VERSION" >&2
  exit 1
}
[[ "$head_commit" == "$tag_commit" ]] || {
  echo "local HEAD does not match the verified remote release commit: $VERSION" >&2
  exit 1
}

printf '%s\n' "$tag_commit"
