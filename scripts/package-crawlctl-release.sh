#!/usr/bin/env bash
set -euo pipefail

version=${1:-X.Y.Z}
version=${version#v}

echo "local release publishing is disabled; run:" >&2
echo "gh workflow run release-unified.yml --repo openclaw/crawlkit -f version=$version" >&2
exit 1
