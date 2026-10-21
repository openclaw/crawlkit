const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
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
    const { github } = fixture({ release: () => ({ data: { id: 123, draft } }) });
    await assert.rejects(preflight({ github, context, version: '0.14.7' }), /already has a GitHub release/);
  });
}

test('the halted v0.14.7 state cannot reach build or draft creation', async () => {
  const { github, calls } = fixture({
    tag: () => ({ data: { object: { type: 'tag', sha: 'f0b0ee206d874feea0304d076246e2ae9277bb9c' } } }),
    release: () => ({ data: { id: 370802867, draft: false } }),
  });
  await assert.rejects(preflight({ github, context, version: '0.14.7' }), /refusing to rebuild/);
  assert.equal(calls.length, 3);
});

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
  assert.match(workflow, /await preflight\(\{ github, context, version: process.env.RELEASE_VERSION, dryRun: process.env.DRY_RUN === 'true' \}\)/);
  assert.match(workflow, /dry-run: \$\{\{ inputs.dry_run \}\}/);
  assert.match(workflow, /require-signed-tag: true/);
});
