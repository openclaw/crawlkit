const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { test } = require('node:test');
const preflight = require('./release-preflight.cjs');

const context = { repo: { owner: 'openclaw', repo: 'crawlkit' } };
const missing = () => { throw Object.assign(new Error('Not Found'), { status: 404 }); };

function fixture({ tag = missing, release = missing } = {}) {
  const calls = [];
  return {
    calls,
    // Only read endpoints exist: mutations are never part of preflight.
    github: { rest: {
      git: { getRef: async (args) => { calls.push(['tag', args]); return tag(); } },
      repos: { getReleaseByTag: async (args) => { calls.push(['release', args]); return release(); } },
    } },
  };
}

for (const version of ['0.14.8', 'v0.14.8', '0.14.8-rc.1', 'v0.14.8+build.1']) {
  test(`unused ${version} reaches the shared release workflow`, async () => {
    const { github, calls } = fixture();
    await preflight({ github, context, version });
    const tag = `v${version.replace(/^v/, '')}`;
    assert.deepEqual(calls, [
      ['tag', { ...context.repo, ref: `tags/${tag}` }],
      ['release', { ...context.repo, tag }],
    ]);
  });
}

for (const kind of ['tag', 'commit']) {
  test(`existing ${kind} stops before rebuilding, even without a GitHub release`, async () => {
    const { github, calls } = fixture({ tag: () => ({ data: { object: { type: kind } } }) });
    await assert.rejects(preflight({ github, context, version: 'v0.14.7' }), /v0\.14\.7 already has a Git tag/);
    assert.equal(calls.length, 1);
  });
}

for (const draft of [false, true]) {
  test(`existing ${draft ? 'draft' : 'public release'} blocks even if its tag is missing`, async () => {
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
  assert.equal(calls.length, 1);
});

for (const endpoint of ['tag', 'release']) {
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

test('workflow gates the reusable job and serializes preflight through publication', () => {
  const workflow = readFileSync(`${__dirname}/../workflows/release-unified.yml`, 'utf8');
  assert.match(workflow, /^concurrency:\n  group: crawlkit-release-\$\{\{ github.repository \}\}\n  cancel-in-progress: false$/m);
  assert.match(workflow, /^  release:\n    needs: preflight\n/m);
  assert.match(workflow, /contents: read/);
  assert.match(workflow, /await preflight\(\{ github, context, version: process.env.RELEASE_VERSION \}\)/);
});
