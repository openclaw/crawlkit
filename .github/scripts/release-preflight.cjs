const { lstatSync, readFileSync, realpathSync } = require('node:fs');
const path = require('node:path');

function componentMetadata(root, version) {
  const directory = path.join(realpathSync(root), 'sessionowner');
  const regular = (file, directoryExpected = false) => {
    let stat;
    try { stat = lstatSync(file); } catch (error) { if (error.code === 'ENOENT') return false; throw error; }
    if (stat.isSymbolicLink() || !(directoryExpected ? stat.isDirectory() : stat.isFile())) throw new Error('component metadata must use regular files and directories, without symlinks');
    return true;
  };
  if (!regular(directory, true) || realpathSync(directory) !== directory) throw new Error('sessionowner metadata directory is required');
  const names = ['CHANGELOG.md', 'package.json', '.release-version', 'VERSION', 'version.txt'];
  const present = new Map(names.map(name => [name, regular(path.join(directory, name))]));
  // The committed source adapter rejects hidden files; do not admit metadata it cannot pack.
  if (present.get('.release-version')) throw new Error('sessionowner/.release-version is incompatible with the artifact path policy; use visible component version metadata');
  if (!present.get('CHANGELOG.md') || !readFileSync(path.join(directory, 'CHANGELOG.md'), 'utf8').trim()) throw new Error('sessionowner/CHANGELOG.md release preparation is required');
  let versions = 0;
  for (const name of names.slice(1)) {
    if (!present.get(name)) continue;
    const contents = readFileSync(path.join(directory, name), 'utf8');
    const actual = name === 'package.json' ? JSON.parse(contents).version : contents.replace(/\s/g, '').replace(/^v/, '');
    if (actual !== version) throw new Error(`sessionowner/${name} version must match the selected release`);
    versions++;
  }
  if (versions === 0) throw new Error('sessionowner component version metadata release preparation is required');
  // The shared workflow owns the dated changelog-section parser and signature check.
}

module.exports = async function preflight({ github, context, version, dryRun = false, component = 'crawlkit', root = process.cwd() }) {
  // Match the reusable workflow's version canonicalization before any lookup.
  if (typeof version !== 'string' || version !== version.trim() || !/^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/.test(version)) {
    throw new Error('version must be SemVer, with optional leading v');
  }
  if (typeof dryRun !== 'boolean') throw new Error('dryRun must be a boolean');
  if (!['crawlkit', 'sessionowner'].includes(component)) throw new Error('component must be crawlkit or sessionowner');
  const normalized = version.replace(/^v/, '');
  if (component === 'sessionowner') {
    // Match the committed artifact adapter, without changing legacy root inputs.
    if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(normalized) || normalized.length > 100 || normalized.split('-').slice(1).join('-').split('.').some(part => /^0\d+$/.test(part))) throw new Error('component version must be canonical SemVer without build metadata');
    componentMetadata(root, normalized);
  }
  // Dry runs build the current protected head without touching the version tag.
  if (dryRun) return component === 'sessionowner' ? normalized : undefined;
  const tag = `${component === 'sessionowner' ? 'sessionowner/v' : 'v'}${normalized}`;
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
  return component === 'sessionowner' ? normalized : undefined;
};
