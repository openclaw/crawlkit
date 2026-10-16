const assert = require('node:assert/strict');
const { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } = require('node:fs');
const { tmpdir } = require('node:os');
const { join } = require('node:path');
const { spawnSync } = require('node:child_process');
const { test } = require('node:test');

const workflow = readFileSync(join(__dirname, '../workflows/crabbox-hydrate.yml'), 'utf8');

function runStep(name) {
  const lines = workflow.split('\n');
  const step = lines.indexOf(`      - name: ${name}`);
  assert.notEqual(step, -1, `missing workflow step: ${name}`);
  const start = lines.findIndex((line, index) => index > step && line === '        run: |');
  assert.notEqual(start, -1, `missing shell script: ${name}`);
  const script = [];
  for (const line of lines.slice(start + 1)) {
    if (line && !line.startsWith('          ')) break;
    script.push(line.slice(10));
  }
  return script.join('\n');
}

function fixture(t) {
  const home = mkdtempSync(join(tmpdir(), 'crawlkit-hydrate-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const env = {
    PATH: process.env.PATH,
    HOME: home,
    CRABBOX_ID: 'cbx_test123',
    CRABBOX_JOB: 'hydrate',
    CRABBOX_KEEP_ALIVE_MINUTES: '90',
    GITHUB_WORKSPACE: join(home, 'workspace'),
    GITHUB_SHA: 'a'.repeat(40),
    GITHUB_RUN_ID: '123',
  };
  return { home, env, actions: join(home, '.crabbox/actions') };
}

test('hydration publishes the lease-scoped marker and environment used by Crabbox', (t) => {
  const { home, env, actions } = fixture(t);
  const result = spawnSync('bash', ['-c', runStep('Mark Crabbox ready')], { env, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  const marker = readFileSync(join(actions, 'cbx_test123.env'), 'utf8');
  assert.match(marker, /^JOB=hydrate$/m);
  assert.match(marker, /^RUN_ID=123$/m);
  assert.ok(marker.includes(`WORKSPACE=${join(home, 'workspace')}\n`));
  assert.ok(marker.includes(`ENV_FILE=${join(actions, 'cbx_test123.env.sh')}\n`));
  assert.match(readFileSync(join(actions, 'cbx_test123.env.sh'), 'utf8'), /^export GITHUB_SHA=/m);
});

test('the lease-scoped stop signal ends the hydration job immediately', (t) => {
  const { home, env, actions } = fixture(t);
  mkdirSync(actions, { recursive: true });
  writeFileSync(join(actions, 'cbx_test123.stop'), '');
  const bin = join(home, 'bin');
  mkdirSync(bin);
  // A missed stop signal must fail promptly instead of sleeping for 90 minutes.
  writeFileSync(join(bin, 'sleep'), '#!/bin/sh\nexit 42\n', { mode: 0o755 });
  env.PATH = `${bin}:${env.PATH}`;
  const result = spawnSync('bash', ['-c', runStep('Keep Crabbox job alive')], { env, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
});
