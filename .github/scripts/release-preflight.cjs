module.exports = async function preflight({ github, context, version, dryRun = false }) {
  // Match the reusable workflow's version canonicalization before any lookup.
  if (typeof version !== 'string' || version !== version.trim() || !/^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/.test(version)) {
    throw new Error('version must be SemVer, with optional leading v');
  }
  if (typeof dryRun !== 'boolean') throw new Error('dryRun must be a boolean');
  // Dry runs build the current protected head without touching the version tag.
  if (dryRun) return;
  const tag = `v${version.replace(/^v/, '')}`;
  const requireAbsent = async (lookup, kind) => {
    try {
      await lookup();
    } catch (error) {
      if (error.status === 404) return;
      throw error;
    }
    throw new Error(`${tag} already has a ${kind}; refusing to rebuild an existing release version. Use a new patch version, or reconcile the original run without rebuilding its verified payload.`);
  };

  // The shared workflow verifies the fleet signature before building or publishing.
  const ref = await github.rest.git.getRef({
    ...context.repo,
    ref: `tags/${tag}`,
  });
  if (ref.data.object.type !== 'tag') throw new Error(`${tag} must be an annotated fleet-signed tag`);
  const annotated = await github.rest.git.getTag({
    ...context.repo,
    tag_sha: ref.data.object.sha,
  });
  if (annotated.data.object.type !== 'commit' || annotated.data.object.sha !== context.sha) {
    throw new Error(`${tag} must point directly to the release workflow commit`);
  }
  await requireAbsent(() => github.rest.repos.getReleaseByTag({
    ...context.repo,
    tag,
  }), 'GitHub release');
};
