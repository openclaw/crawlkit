import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, utimesSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync, gunzipSync } from 'node:zlib';
import { packageRelease, readArchive, verifyRelease } from './package.mjs';

const helper = fileURLToPath(new URL('./package.mjs', import.meta.url));
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const json = value => Buffer.from(JSON.stringify(value) + '\n');
const version = '1.0.0';
const git = (repo, ...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', timeout: 10000 }).trim();
const nativePackage = { name: '@openclaw/crawlkit-sessionowner-binding', version: '1.0.0-candidate.0', private: true, type: 'module', exports: { '.': { types: './index.d.ts', default: './index.mjs' } }, scripts: { test: 'node --test index.test.mjs' }, engines: { node: '>=24' } };

function fixture(t) {
  const directory = mkdtempSync(path.join(tmpdir(), 'sessionowner-release-test-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const repo = path.join(directory, 'repo');
  mkdirSync(repo);
  const put = (name, bytes) => { const target = path.join(repo, name); mkdirSync(path.dirname(target), { recursive: true }); writeFileSync(target, bytes); };
  put('LICENSE', 'MIT fixture license, not a real release.\n');
  put('sessionowner/go.mod', 'module github.com/openclaw/crawlkit/sessionowner\n\ngo 1.27.0\n');
  put('sessionowner/example.go', 'package sessionowner\n');
  put('sessionowner/bindings/node/package.json', json(nativePackage));
  put('sessionowner/bindings/node/index.mjs', '// Synthetic fixture only; no import or execution.\n');
  put('sessionowner/bindings/node/index.d.ts', 'export declare const fixture: true;\n');
  put('sessionowner/bindings/node/schema-closure.mjs', '// Synthetic fixture only.\n');
  const files = ['request', 'response', 'cancel', 'adapter', 'registry-policy'].map(name => {
    const id = `urn:openclaw:sessionowner:abi-v1:${name}`;
    const bytes = json({ $id: id, type: 'object', additionalProperties: false });
    put(`sessionowner/schema/v1/${name}.schema.json`, bytes);
    return { file: `${name}.schema.json`, id, sha256: digest(bytes), bytes: bytes.length };
  });
  put('sessionowner/schema/v1/manifest.json', json({ manifestVersion: 'sessionowner-abi-manifest/1', schemaVersion: '1.0.0', publicProtocol: 'sessionowner/1', privateProtocol: 'sessionowner-adapter/1', files }));
  git(repo, 'init', '--initial-branch=main');
  git(repo, 'config', 'user.name', 'Release fixture');
  git(repo, 'config', 'user.email', 'fixture@example.invalid');
  git(repo, 'remote', 'add', 'origin', 'https://github.com/openclaw/crawlkit.git');
  const commit = () => { git(repo, 'add', '--', '.'); git(repo, 'commit', '-m', 'synthetic release fixture'); return git(repo, 'rev-parse', 'HEAD'); };
  const head = commit();
  return { directory, repo, put, commit, head, output: path.join(directory, 'sessionowner.tar.gz') };
}

function packed(t) {
  const f = fixture(t);
  const result = packageRelease({ repo: f.repo, commit: f.head, version, output: f.output });
  return { ...f, result, bytes: readFileSync(f.output) };
}

function repack(t, files) {
  const directory = mkdtempSync(path.join(tmpdir(), 'sessionowner-release-repack-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  for (const [name, file] of files) {
    assert.ok(!name.includes('..') && !path.isAbsolute(name));
    const target = path.join(directory, name);
    mkdirSync(path.dirname(target), { recursive: true });
    writeFileSync(target, file.bytes);
    chmodSync(target, file.mode);
    utimesSync(target, 1, 1);
  }
  return gzipSync(execFileSync('tar', ['--format=ustar', '--no-recursion', '-cf', '-', '-C', directory, '--', ...[...files.keys()].sort()], { timeout: 10000 }));
}

function mutateHeader(bytes, change) {
  const tar = gunzipSync(bytes);
  change(tar.subarray(0, 512));
  tar.fill(32, 148, 156);
  const sum = tar.subarray(0, 512).reduce((total, byte) => total + byte, 0);
  tar.write(sum.toString(8).padStart(6, '0') + '\0 ', 148, 'ascii');
  return gzipSync(tar);
}

test('one committed envelope preserves source, schema-relative private binding and separate versions', t => {
  const f = packed(t);
  const result = verifyRelease({ repo: f.repo, commit: f.head, version, bytes: f.bytes });
  assert.equal(result.repository, 'openclaw/crawlkit');
  assert.equal(result.commit, f.head);
  assert.equal(result.componentTree, git(f.repo, 'rev-parse', `${f.head}:sessionowner`));
  assert.equal(result.module.tag, 'sessionowner/v1.0.0');
  assert.equal(result.binding.version, '1.0.0-candidate.0');
  assert.equal(result.binding.registryPublish, false);
  assert.equal(result.abi.files.length, 5);
  const envelope = readArchive(f.bytes);
  assert.equal(envelope.size, 4);
  const binding = readArchive(envelope.get('private-node.tgz').bytes);
  const metadata = JSON.parse(binding.get('package/package.json').bytes);
  assert.equal(metadata.private, true);
  assert.equal(metadata.scripts, undefined);
  assert.equal(metadata.exports['.'].default, './bindings/node/index.mjs');
  assert.deepEqual(JSON.parse(binding.get('package/bindings/node/package.json').bytes), nativePackage);
  assert.ok(binding.has('package/schema/v1/request.schema.json'));
  assert.deepEqual(binding.get('package/LICENSE').bytes, readFileSync(path.join(f.repo, 'LICENSE')));
  const other = packageRelease({ repo: f.repo, commit: f.head, version: 'v1.0.0', output: path.join(f.directory, 'second.tar.gz') });
  assert.equal(other.sha256, f.result.sha256);
  assert.equal(git(f.repo, 'status', '--porcelain'), '');
});

test('CLI consumes only the existing generated-file environment seam', t => {
  const f = fixture(t);
  const stdout = execFileSync(process.execPath, [helper, '--version', version, '--commit', f.head], {
    cwd: f.repo, env: { ...process.env, RELEASE_ASSET_PATH: f.output }, encoding: 'utf8', timeout: 20000,
  });
  assert.equal(JSON.parse(stdout).commit, f.head);
  assert.ok(existsSync(f.output));
  assert.equal(git(f.repo, 'status', '--porcelain'), '');
});

test('dirty, untracked and ignored component extras fail without producing an asset', async t => {
  for (const kind of ['dirty', 'untracked', 'ignored']) await t.test(kind, t => {
    const f = fixture(t);
    if (kind === 'ignored') { f.put('.gitignore', 'sessionowner/ignored.txt\n'); f.head = f.commit(); }
    f.put(kind === 'dirty' ? 'sessionowner/example.go' : `sessionowner/${kind}.txt`, 'changed or extra\n');
    assert.throws(() => packageRelease({ repo: f.repo, commit: f.head, version, output: f.output }), /dirty_or_extra_source/);
    assert.ok(!existsSync(f.output));
  });
});

test('exact HEAD and repository identity are required', t => {
  const f = fixture(t);
  assert.throws(() => packageRelease({ repo: f.repo, commit: '0'.repeat(40), version, output: f.output }), /commit_must_equal_head/);
  git(f.repo, 'remote', 'set-url', 'origin', 'https://github.com/other/crawlkit.git');
  assert.throws(() => packageRelease({ repo: f.repo, commit: f.head, version, output: f.output }), /unexpected_repository/);
  assert.ok(!existsSync(f.output));
});

test('committed links, local Go overrides and credential material are rejected', async t => {
  for (const kind of ['symlink', 'replace', 'key-file', 'key-content', 'token', 'workspace']) await t.test(kind, t => {
    const f = fixture(t);
    if (kind === 'symlink') symlinkSync('/does/not/exist', path.join(f.repo, 'sessionowner/link'));
    if (kind === 'replace') f.put('sessionowner/go.mod', 'module github.com/openclaw/crawlkit/sessionowner\nreplace example.invalid/m => ../local\n');
    if (kind === 'key-file') f.put('sessionowner/signing.pem', 'fixture');
    if (kind === 'key-content') f.put('sessionowner/config.txt', ['-----BEGIN', 'PRIVATE KEY-----'].join(' '));
    if (kind === 'token') f.put('sessionowner/config.txt', 'ghp_' + 'a'.repeat(40));
    if (kind === 'workspace') f.put('sessionowner/go.work', 'go 1.27.0\n');
    f.head = f.commit();
    assert.throws(() => packageRelease({ repo: f.repo, commit: f.head, version, output: f.output }), /source_must_be_regular|module_identity_or_local_override|forbidden_source_path|credential_material/);
    assert.ok(!existsSync(f.output));
  });
});

test('private metadata, lifecycle and schema digest drift fail closed', async t => {
  for (const kind of ['public', 'lifecycle', 'dependency', 'schema']) await t.test(kind, t => {
    const f = fixture(t), metadata = structuredClone(nativePackage);
    if (kind === 'public') metadata.private = false;
    if (kind === 'lifecycle') metadata.scripts.postinstall = 'do-not-execute';
    if (kind === 'dependency') metadata.dependencies = { invalid: '*' };
    if (kind !== 'schema') f.put('sessionowner/bindings/node/package.json', json(metadata));
    else f.put('sessionowner/schema/v1/request.schema.json', json({ $id: 'changed' }));
    f.head = f.commit();
    assert.throws(() => packageRelease({ repo: f.repo, commit: f.head, version, output: f.output }), /unexpected_binding_metadata|schema_digest_mismatch/);
  });
});

test('archive reader rejects traversal, links, high-bit paths, duplicate paths and corrupt headers', async t => {
  const bytes = repack(t, new Map([['safe.txt', { bytes: Buffer.from('fixture'), mode: 0o644 }]]));
  for (const name of ['../escape', '/absolute', 'folder/../escape', 'folder\\escape', '.env']) await t.test(name, () => {
    const changed = mutateHeader(bytes, header => { header.fill(0, 0, 100); header.write(name, 0, 'ascii'); });
    assert.throws(() => readArchive(changed), /unsafe_archive_path/);
  });
  for (const type of ['1', '2', 'x']) await t.test(`type ${type}`, () => {
    assert.throws(() => readArchive(mutateHeader(bytes, header => { header[156] = type.charCodeAt(0); })), /archive_must_be_regular/);
  });
  assert.throws(() => readArchive(mutateHeader(bytes, header => { header[0] |= 128; })), /invalid_tar_string/);
  const raw = gunzipSync(bytes), duplicate = Buffer.concat([raw.subarray(0, 1024), raw.subarray(0, 1024), Buffer.alloc(1024)]);
  assert.throws(() => readArchive(gzipSync(duplicate)), /duplicate_or_excess_archive_path/);
  raw[0] ^= 1;
  assert.throws(() => readArchive(gzipSync(raw)), /tar_checksum_mismatch/);
});

test('external expected identity and full inventory reject envelope tampering', async t => {
  const f = packed(t);
  for (const kind of ['manifest', 'checksum', 'extra', 'source', 'binding']) await t.test(kind, t => {
    const envelope = readArchive(f.bytes);
    if (kind === 'manifest') { const value = JSON.parse(envelope.get('manifest.json').bytes); value.commit = '0'.repeat(40); envelope.get('manifest.json').bytes = json(value); }
    if (kind === 'checksum') envelope.get('SHA256SUMS').bytes = Buffer.from('wrong\n');
    if (kind === 'extra') envelope.set('extra.txt', { bytes: Buffer.from('extra'), mode: 0o644 });
    if (kind === 'source') {
      const inner = readArchive(envelope.get('source.tar.gz').bytes);
      inner.get('source/sessionowner/example.go').bytes = Buffer.from('altered\n');
      envelope.get('source.tar.gz').bytes = repack(t, inner);
    }
    if (kind === 'binding') {
      const inner = readArchive(envelope.get('private-node.tgz').bytes);
      inner.get('package/package.json').bytes = json({ ...nativePackage, private: false });
      envelope.get('private-node.tgz').bytes = repack(t, inner);
    }
    assert.throws(() => verifyRelease({ repo: f.repo, commit: f.head, version, bytes: repack(t, envelope) }));
  });
  assert.throws(() => verifyRelease({ repo: f.repo, commit: f.head, version: '1.0.1', bytes: f.bytes }), /manifest_identity_mismatch/);
});

test('invalid versions and unsafe or occupied output paths never overwrite files', t => {
  const f = fixture(t);
  for (const value of ['../1.0.0', 'sessionowner/v1.0.0', '01.0.0', '1.0.0-01', '1.0.0+build', '', null]) {
    assert.throws(() => packageRelease({ repo: f.repo, commit: f.head, version: value, output: f.output }), /invalid_version/);
  }
  assert.throws(() => packageRelease({ repo: f.repo, commit: f.head, version, output: path.join(f.repo, 'output.tar.gz') }), /output_must_be_outside_checkout/);
  writeFileSync(f.output, 'keep');
  assert.throws(() => packageRelease({ repo: f.repo, commit: f.head, version, output: f.output }), /EEXIST/);
  assert.equal(readFileSync(f.output, 'utf8'), 'keep');
  rmSync(f.output);
  const target = path.join(f.directory, 'target');
  writeFileSync(target, 'keep target');
  symlinkSync(target, f.output);
  assert.throws(() => packageRelease({ repo: f.repo, commit: f.head, version, output: f.output }), /EEXIST/);
  assert.equal(readFileSync(target, 'utf8'), 'keep target');
});
