const assert = require('node:assert/strict');
const { readFileSync, writeFileSync, mkdirSync, mkdtempSync, rmSync, symlinkSync } = require('node:fs');
const { tmpdir } = require('node:os');
const path = require('node:path');
const { test } = require('node:test');
const preflight = require('./release-preflight.cjs');

const context = { repo: { owner: 'openclaw', repo: 'crawlkit' }, sha: 'a'.repeat(40) };
const missing = () => { throw Object.assign(new Error('Not Found'), { status: 404 }); };

function fixture({ tag = () => ({ data: { object: { type: 'tag', sha: 'b'.repeat(40) } } }), annotated = () => ({ data: { object: { type: 'commit', sha: context.sha } } }), release = missing } = {}) {
  const calls = [];
  return {
    calls,
    // Only read endpoints exist: mutations are never part of preflight.
    github: { rest: {
      git: {
        getRef: async (args) => { calls.push(['tag', args]); return tag(); },
        getTag: async (args) => { calls.push(['annotated', args]); return annotated(); },
      },
      repos: { getReleaseByTag: async (args) => { calls.push(['release', args]); return release(); } },
    } },
  };
}

for (const version of ['0.14.8', 'v0.14.8', '0.14.8-rc.1', 'v0.14.8+build.1']) {
  test(`annotated ${version} at the workflow commit reaches signature verification`, async () => {
    const { github, calls } = fixture();
    await preflight({ github, context, version });
    const tag = `v${version.replace(/^v/, '')}`;
    assert.deepEqual(calls, [
      ['tag', { ...context.repo, ref: `tags/${tag}` }],
      ['annotated', { ...context.repo, tag_sha: 'b'.repeat(40) }],
      ['release', { ...context.repo, tag }],
    ]);
  });
}

for (const kind of ['commit', 'tree', 'blob']) {
  test(`non-annotated ${kind} stops before building`, async () => {
    const { github, calls } = fixture({ tag: () => ({ data: { object: { type: kind } } }) });
    await assert.rejects(preflight({ github, context, version: 'v0.14.7' }), /must be an annotated fleet-signed tag/);
    assert.equal(calls.length, 1);
  });
}

for (const draft of [false, true]) {
  test(`existing ${draft ? 'draft' : 'public release'} blocks publication`, async () => {
    const { github, calls } = fixture({ release: () => ({ data: { id: 123, draft } }) });
    await assert.rejects(preflight({ github, context, version: '0.14.7' }), /already has a GitHub release; refusing to rebuild/);
    assert.equal(calls.length, 3);
  });
}

for (const endpoint of ['tag', 'annotated', 'release']) {
  for (const status of [401, 403, 429, 500]) {
    test(`${endpoint} HTTP ${status} fails closed`, async () => {
      const error = Object.assign(new Error('API unavailable'), { status });
      const { github } = fixture({ [endpoint]: () => { throw error; } });
      await assert.rejects(preflight({ github, context, version: '0.14.8' }), (observed) => observed === error);
    });
  }
}

for (const version of ['', 'latest', 'v1.2', ' v0.14.7', 'v0.14.7\n', 'v0.14.7/other', undefined]) {
  test(`invalid input ${JSON.stringify(version)} does not call GitHub`, async () => {
    const { github, calls } = fixture();
    await assert.rejects(preflight({ github, context, version }), /version must be SemVer/);
    assert.equal(calls.length, 0);
  });
}

test('dry runs accept an existing version without querying or modifying it', async () => {
  const { github, calls } = fixture({
    tag: () => ({ data: { object: { type: 'tag' } } }),
    release: () => ({ data: { id: 123, draft: false } }),
  });
  await preflight({ github, context, version: 'v0.17.0', dryRun: true });
  assert.deepEqual(calls, []);
});

test('dry runs still reject invalid versions', async () => {
  const { github, calls } = fixture();
  await assert.rejects(preflight({ github, context, version: 'latest', dryRun: true }), /version must be SemVer/);
  assert.deepEqual(calls, []);
});

test('publication requires a pre-created tag', async () => {
  const { github } = fixture({ tag: missing });
  await assert.rejects(preflight({ github, context, version: 'v0.17.0', dryRun: false }), /Not Found/);
});

for (const object of [{ type: 'commit', sha: 'c'.repeat(40) }, { type: 'tag', sha: context.sha }]) {
  test(`rejects a tag that does not directly freeze the workflow commit (${object.type})`, async () => {
    const { github } = fixture({ annotated: () => ({ data: { object } }) });
    await assert.rejects(preflight({ github, context, version: '0.17.2' }), /must point directly to the release workflow commit/);
  });
}

for (const dryRun of ['true', 'false', 1, null]) {
  test(`non-boolean dryRun ${JSON.stringify(dryRun)} fails closed`, async () => {
    const { github, calls } = fixture();
    await assert.rejects(preflight({ github, context, version: 'v0.17.0', dryRun }), /dryRun must be a boolean/);
    assert.deepEqual(calls, []);
  });
}

test('workflow gates the reusable job and serializes preflight through publication', () => {
  const workflow = readFileSync(`${__dirname}/../workflows/release-unified.yml`, 'utf8');
  assert.match(workflow, /^concurrency:\n  group: crawlkit-release-\$\{\{ github.repository \}\}\n  cancel-in-progress: false$/m);
  assert.match(workflow, /^  release:\n    needs: preflight\n/m);
  assert.match(workflow, /contents: read/);
  assert.match(workflow, /dry_run:\n\s+description:.*\n\s+type: boolean\n\s+default: false/);
  assert.match(workflow, /DRY_RUN: \$\{\{ inputs.dry_run \}\}/);
  assert.match(workflow, /await preflight\(\{ github, context, version: process.env.RELEASE_VERSION, dryRun: process.env.DRY_RUN === 'true', component: process.env.RELEASE_COMPONENT \}\)/);
  assert.match(workflow, /dry-run: \$\{\{ inputs.dry_run \}\}/);
  assert.match(workflow, /require-signed-tag: true/);
});


function componentRoot(t, metadata = 'VERSION') {
  const root = mkdtempSync(path.join(tmpdir(), 'sessionowner-preflight-test-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const directory = path.join(root, 'sessionowner');
  mkdirSync(directory);
  writeFileSync(path.join(directory, 'CHANGELOG.md'), '# Fixture only\n\n## 1.0.0 - 2026-11-06\nSynthetic release notes.\n');
  writeFileSync(path.join(directory, metadata), metadata === 'package.json' ? JSON.stringify({ version: '1.0.0' }) : '1.0.0\n');
  return { root, directory };
}

for (const version of ['1.0.0', 'v1.0.0']) {
  test(`component ${version} uses only the namespaced exact-commit tag and release`, async t => {
    const { root } = componentRoot(t);
    const { github, calls } = fixture();
    assert.equal(await preflight({ github, context, version, component: 'sessionowner', root }), '1.0.0');
    assert.deepEqual(calls, [
      ['tag', { ...context.repo, ref: 'tags/sessionowner/v1.0.0' }],
      ['annotated', { ...context.repo, tag_sha: 'b'.repeat(40) }],
      ['release', { ...context.repo, tag: 'sessionowner/v1.0.0' }],
    ]);
  });
}

test('component dry run validates local metadata but has no tag, release or latest lookup', async t => {
  const { root } = componentRoot(t);
  const { github, calls } = fixture({ release: () => ({ data: { id: 123 } }) });
  assert.equal(await preflight({ github, context, version: 'v1.0.0', component: 'sessionowner', root, dryRun: true }), '1.0.0');
  assert.deepEqual(calls, []);
});

for (const metadata of ['VERSION', 'version.txt', 'package.json']) {
  test(`component accepts its own ${metadata} without reading root or private-binding versions`, async t => {
    const { root } = componentRoot(t, metadata);
    writeFileSync(path.join(root, 'VERSION'), '0.0.1\n');
    const { github, calls } = fixture();
    await preflight({ github, context, version: '1.0.0', component: 'sessionowner', root, dryRun: true });
    assert.deepEqual(calls, []);
  });
}

for (const defect of ['missing-changelog', 'empty-changelog', 'missing-version', 'wrong-version', 'directory', 'changelog-symlink', 'version-symlink', 'component-symlink']) {
  test(`component ${defect} fails before remote reads, including dry runs`, async t => {
    const { root, directory } = componentRoot(t);
    const changelog = path.join(directory, 'CHANGELOG.md');
    const versionFile = path.join(directory, 'VERSION');
    if (defect === 'missing-changelog') rmSync(changelog);
    if (defect === 'empty-changelog') writeFileSync(changelog, '\n');
    if (defect === 'missing-version') rmSync(versionFile);
    if (defect === 'wrong-version') writeFileSync(versionFile, '9.9.9\n');
    if (defect === 'directory') { rmSync(versionFile); mkdirSync(versionFile); }
    if (defect === 'changelog-symlink') { rmSync(changelog); symlinkSync('../VERSION', changelog); }
    if (defect === 'version-symlink') { rmSync(versionFile); symlinkSync('../VERSION', versionFile); }
    if (defect === 'component-symlink') { rmSync(directory, { recursive: true }); symlinkSync(root, directory); }
    for (const dryRun of [true, false]) {
      const { github, calls } = fixture();
      await assert.rejects(preflight({ github, context, version: '1.0.0', component: 'sessionowner', root, dryRun }), /metadata|release preparation|version must match/);
      assert.deepEqual(calls, []);
    }
  });
}

test('hidden release metadata is refused before the unchanged artifact adapter would reject it', async t => {
  const { root } = componentRoot(t, '.release-version');
  const { github, calls } = fixture();
  await assert.rejects(preflight({ github, context, version: '1.0.0', component: 'sessionowner', root, dryRun: true }), /artifact path policy/);
  assert.deepEqual(calls, []);
});

for (const component of ['other', '../sessionowner', 'sessionowner/v', '', null, 1]) {
  test(`unknown component ${JSON.stringify(component)} cannot fall through to root release`, async () => {
    const { github, calls } = fixture();
    await assert.rejects(preflight({ github, context, version: '1.0.0', component, dryRun: true }), /component must be/);
    assert.deepEqual(calls, []);
  });
}

for (const version of ['sessionowner/v1.0.0', '01.0.0', '1.0.0-01', '1.0.0+build', "1.0.0'; echo injected", '1.0.0\n']) {
  test(`unsafe or unsupported component version ${JSON.stringify(version)} cannot enter a generated command`, async t => {
    const { root } = componentRoot(t);
    const { github, calls } = fixture();
    await assert.rejects(preflight({ github, context, version, component: 'sessionowner', root, dryRun: true }), /version must be/);
    assert.deepEqual(calls, []);
  });
}

for (const failure of ['non-annotated', 'wrong-head', 'existing-release', 'lookup-error']) {
  test(`component retains the ${failure} fail-closed gate`, async t => {
    const { root } = componentRoot(t);
    const overrides = {};
    if (failure === 'non-annotated') overrides.tag = () => ({ data: { object: { type: 'commit' } } });
    if (failure === 'wrong-head') overrides.annotated = () => ({ data: { object: { type: 'commit', sha: 'c'.repeat(40) } } });
    if (failure === 'existing-release') overrides.release = () => ({ data: { id: 123, draft: true } });
    if (failure === 'lookup-error') overrides.release = () => { throw Object.assign(new Error('forbidden'), { status: 403 }); };
    const { github, calls } = fixture(overrides);
    await assert.rejects(preflight({ github, context, version: '1.0.0', component: 'sessionowner', root }));
    assert.equal(calls[0][1].ref, 'tags/sessionowner/v1.0.0');
  });
}

test('explicit and omitted root selectors retain legacy version and dry-run behavior without component metadata', async () => {
  for (const component of [undefined, 'crawlkit']) {
    const { github, calls } = fixture();
    await preflight({ github, context, version: 'v0.17.0+build.1', component, dryRun: true, root: '/not-a-component-checkout' });
    assert.deepEqual(calls, []);
  }
});

test('component jobs use the frozen source interface without root build, secrets or latest selection', () => {
  const workflow = readFileSync(`${__dirname}/../workflows/release-unified.yml`, 'utf8');
  const rootJobs = workflow.split('\n  component-preflight:\n')[0];
  const componentJobs = workflow.split('\n  component-preflight:\n')[1];
  assert.match(workflow, /component:\n\s+description:.*\n\s+type: choice\n\s+default: crawlkit\n\s+options:\n\s+- crawlkit\n\s+- sessionowner/);
  assert.equal((rootJobs.match(/if: \$\{\{ inputs\.component != 'sessionowner' \}\}/g) || []).length, 2);
  assert.match(rootJobs, /RELEASE_COMPONENT: \$\{\{ inputs.component \|\| 'crawlkit' \}\}/);
  assert.match(rootJobs, /release-go-cli.yml@v1/);
  assert.match(componentJobs, /runs-on: ubuntu-latest/);
  assert.match(componentJobs, /needs: component-preflight/);
  assert.match(componentJobs, /release-source.yml@a4662ed0e732860a19f6a487151486faaf5ec2e0/);
  assert.match(componentJobs, /Provisional local candidate, not remotely qualified/);
  for (const line of ["tag-prefix: sessionowner/v", 'component-path: sessionowner', "make-latest: 'false'", `go-modules: '["sessionowner"]'`, 'require-signed-tag: true', 'source-archives: false', "homebrew-formula: ''", 'dry_run: ${{ inputs.dry_run }}']) assert.ok(componentJobs.includes(line), line);
  assert.match(componentJobs, /version: \$\{\{ needs\.component-preflight\.outputs\.version \}\}/);
  assert.match(componentJobs, /--version '\$\{\{ needs\.component-preflight\.outputs\.version \}\}' --commit "\$\(git rev-parse HEAD\)"/);
  assert.match(componentJobs, /generated-asset: sessionowner-\$\{\{ needs\.component-preflight\.outputs\.version \}\}\.tar\.gz/);
  assert.doesNotMatch(componentJobs, /go build|setup-go|Rosetta|notary|release-go-cli|MACOS_SIGNING|ASC_PRIVATE|secrets:|go-modules: '\["\."\]'/);
  assert.doesNotMatch(componentJobs, /package\.test\.mjs|sessionowner\/v\$\{\{ inputs\.version/);
});
