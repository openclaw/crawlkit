module.exports = async function preflight({ github, context, version }) {
  // Match the reusable workflow's version canonicalization before any lookup.
  if (typeof version !== 'string' || version !== version.trim() || !/^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/.test(version)) {
    throw new Error('version must be SemVer, with optional leading v');
  }
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

  // Tags are public Go module versions even without a published GitHub release.
  // The shared workflow creates the tag before creating any draft or assets.
  await requireAbsent(() => github.rest.git.getRef({
    ...context.repo,
    ref: `tags/${tag}`,
  }), 'Git tag');
  await requireAbsent(() => github.rest.repos.getReleaseByTag({
    ...context.repo,
    tag,
  }), 'GitHub release');
};
