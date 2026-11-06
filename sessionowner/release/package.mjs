// Offline adapter for release-source's one RELEASE_ASSET_PATH file.
// This is not a publisher, installer, or provider/binding execution path.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, lstatSync, mkdirSync, mkdtempSync, realpathSync, rmSync, utimesSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync, gunzipSync } from 'node:zlib';

const REPOSITORY = 'openclaw/crawlkit';
const MODULE = 'github.com/openclaw/crawlkit/sessionowner';
const BINDING = '@openclaw/crawlkit-sessionowner-binding';
const MAX_BYTES = 32 * 1024 * 1024;
const MAX_FILE = 8 * 1024 * 1024;
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
const json = value => Buffer.from(JSON.stringify(value, null, 2) + '\n');
const fail = code => { throw new Error(code); };
const same = (a, b, code) => { if (!Buffer.from(a).equals(Buffer.from(b))) fail(code); };
const git = (repo, ...args) => execFileSync('git', ['-C', repo, ...args], { maxBuffer: MAX_BYTES, timeout: 10000 });
const text = (repo, ...args) => git(repo, ...args).toString('utf8').trim();

function safePath(name) {
  if (typeof name !== 'string' || !/^[A-Za-z0-9_.\/-]+$/.test(name) || name.length > 240 ||
      name.split('/').some(part => !part || part === '.' || part === '..' || part.startsWith('.'))) fail('unsafe_archive_path');
  if (/(^|\/)(?:node_modules|vendor)(\/|$)/.test(name) ||
      /(?:^|\/)(?:go\.work(?:\.sum)?|credentials(?:\.[^.]+)?|id_(?:rsa|ed25519)|[^/]+\.(?:pem|p8|p12|key))$/i.test(name)) fail('forbidden_source_path');
  return name;
}

function versionOf(value) {
  if (typeof value !== 'string') fail('invalid_version');
  const version = value.replace(/^v/, '');
  if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(version) ||
      version.length > 100 || version.split('-').slice(1).join('-').split('.').some(part => /^0\d+$/.test(part))) fail('invalid_version');
  return version;
}

function credentialFree(bytes) {
  const content = bytes.toString('utf8');
  if (/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(content) || /\bgh[pousr]_[A-Za-z0-9]{30,}\b/.test(content) ||
      /\bAKIA[A-Z0-9]{16}\b/.test(content)) fail('credential_material');
}

function snapshot(repo, commit) {
  repo = realpathSync(repo);
  if (!/^[a-f0-9]{40}$/.test(commit ?? '') || text(repo, 'rev-parse', 'HEAD') !== commit) fail('commit_must_equal_head');
  if (text(repo, 'rev-parse', '--show-toplevel') !== repo) fail('repository_root_required');
  if (!['https://github.com/openclaw/crawlkit.git', 'https://github.com/openclaw/crawlkit', 'git@github.com:openclaw/crawlkit.git'].includes(text(repo, 'remote', 'get-url', 'origin'))) fail('unexpected_repository');
  if (text(repo, 'status', '--porcelain=v1', '--untracked-files=all') ||
      text(repo, 'ls-files', '--others', '--ignored', '--exclude-standard', '--', 'sessionowner')) fail('dirty_or_extra_source');
  const rows = git(repo, 'ls-tree', '-r', '-z', commit, '--', 'sessionowner', 'LICENSE').toString('utf8').split('\0').filter(Boolean);
  if (rows.length === 0 || rows.length > 1024) fail('source_file_count');
  const files = new Map();
  let total = 0;
  for (const row of rows) {
    const match = /^(100644|100755) blob ([a-f0-9]{40})\t(.+)$/.exec(row);
    if (!match) fail('source_must_be_regular');
    const [, gitMode, blob, name] = match;
    safePath(name);
    const bytes = git(repo, 'cat-file', 'blob', blob);
    total += bytes.length;
    if (bytes.length > MAX_FILE || total > MAX_BYTES / 2) fail('source_too_large');
    credentialFree(bytes);
    files.set(name, { bytes, mode: Number.parseInt(gitMode.slice(3), 8), blob });
  }
  const need = name => files.get(name)?.bytes ?? fail('required_source_missing');
  need('LICENSE');
  const module = need('sessionowner/go.mod').toString('utf8');
  if (!new RegExp(`^module ${MODULE.replaceAll('.', '\\.')}\\s*$`, 'm').test(module) ||
      (module.match(/^module\s/gm) ?? []).length !== 1 || /^\s*(?:replace|exclude)\b/m.test(module)) fail('module_identity_or_local_override');
  const metadata = JSON.parse(need('sessionowner/bindings/node/package.json'));
  if (metadata.name !== BINDING || metadata.private !== true || metadata.type !== 'module' ||
      metadata.engines?.node !== '>=24' || Object.keys(metadata).some(key => !['name', 'version', 'private', 'type', 'exports', 'scripts', 'engines'].includes(key)) ||
      Object.keys(metadata.scripts ?? {}).some(key => key !== 'test')) fail('unexpected_binding_metadata');
  versionOf(metadata.version);
  assert.deepEqual(metadata.exports, { '.': { types: './index.d.ts', default: './index.mjs' } }, 'binding exports changed');
  const schemaBytes = need('sessionowner/schema/v1/manifest.json');
  const abi = JSON.parse(schemaBytes);
  if (abi.manifestVersion !== 'sessionowner-abi-manifest/1' || abi.schemaVersion !== '1.0.0' ||
      abi.publicProtocol !== 'sessionowner/1' || abi.privateProtocol !== 'sessionowner-adapter/1' || !Array.isArray(abi.files) || abi.files.length !== 5) fail('unexpected_abi_manifest');
  const schemas = new Set();
  for (const item of abi.files) {
    if (!/^(?:request|response|cancel|adapter|registry-policy)\.schema\.json$/.test(item.file) || schemas.has(item.file)) fail('unexpected_schema_file');
    schemas.add(item.file);
    const bytes = need(`sessionowner/schema/v1/${item.file}`);
    if (sha256(bytes) !== item.sha256 || bytes.length !== item.bytes || JSON.parse(bytes).$id !== item.id) fail('schema_digest_mismatch');
  }
  return { repo, commit, files, metadata, abi, schemaBytes, tree: text(repo, 'rev-parse', `${commit}:sessionowner`), timestamp: Number(text(repo, 'show', '-s', '--format=%ct', commit)) };
}

// Only the regular-file USTAR subset emitted below is accepted. No extraction.
export function readArchive(compressed) {
  if (compressed.length > MAX_BYTES) fail('archive_too_large');
  const tar = gunzipSync(compressed, { maxOutputLength: MAX_BYTES });
  if (tar.length < 1024 || tar.length % 512 !== 0) fail('invalid_tar_length');
  const files = new Map(), names = new Set();
  const string = bytes => {
    if (bytes.some(byte => byte > 127)) fail('invalid_tar_string');
    const end = bytes.indexOf(0);
    if (end >= 0 && bytes.subarray(end).some(byte => byte !== 0 && byte !== 32)) fail('invalid_tar_string');
    return bytes.subarray(0, end < 0 ? bytes.length : end).toString('ascii');
  };
  const octal = bytes => { const value = string(bytes).trim(); if (!/^[0-7]+$/.test(value)) fail('invalid_tar_number'); return Number.parseInt(value, 8); };
  let offset = 0;
  while (offset < tar.length) {
    const header = tar.subarray(offset, offset + 512);
    if (header.every(byte => byte === 0)) {
      if (tar.length - offset < 1024 || !tar.subarray(offset).every(byte => byte === 0)) fail('invalid_tar_terminator');
      return files;
    }
    if (header.subarray(257, 263).toString('ascii') !== 'ustar\0' || header.subarray(263, 265).toString('ascii') !== '00') fail('unsupported_tar_format');
    const expectedSum = octal(header.subarray(148, 156));
    const actualSum = header.reduce((sum, byte, index) => sum + (index >= 148 && index < 156 ? 32 : byte), 0);
    if (actualSum !== expectedSum) fail('tar_checksum_mismatch');
    if (header[156] !== 0 && header[156] !== 48 || string(header.subarray(157, 257))) fail('archive_must_be_regular');
    const prefix = string(header.subarray(345, 500));
    const name = safePath((prefix ? prefix + '/' : '') + string(header.subarray(0, 100)));
    if (names.has(name.toLowerCase()) || files.size >= 1024) fail('duplicate_or_excess_archive_path');
    names.add(name.toLowerCase());
    const size = octal(header.subarray(124, 136)), mode = octal(header.subarray(100, 108));
    if (size > MAX_FILE || ![0o644, 0o755].includes(mode) || offset + 512 + size > tar.length) fail('invalid_archive_file');
    files.set(name, { bytes: tar.subarray(offset + 512, offset + 512 + size), mode });
    const end = offset + 512 + size;
    offset += 512 + Math.ceil(size / 512) * 512;
    if (!tar.subarray(end, offset).every(byte => byte === 0)) fail('invalid_tar_padding');
  }
  fail('missing_tar_terminator');
}

function archive(files, timestamp) {
  const directory = mkdtempSync(path.join(tmpdir(), 'sessionowner-pack-'));
  try {
    const names = [...files.keys()].sort();
    for (const name of names) {
      safePath(name);
      const target = path.join(directory, name), file = files.get(name);
      mkdirSync(path.dirname(target), { recursive: true });
      writeFileSync(target, file.bytes, { flag: 'wx', mode: file.mode });
      chmodSync(target, file.mode);
      utimesSync(target, timestamp, timestamp);
    }
    const bytes = execFileSync('tar', ['--format=ustar', '--no-recursion', '-cf', '-', '-C', directory, '--', ...names], { maxBuffer: MAX_BYTES, timeout: 10000 });
    return gzipSync(bytes, { level: 9 });
  } finally { rmSync(directory, { recursive: true, force: true }); }
}

function layouts(source) {
  const moduleFiles = new Map([...source.files].map(([name, file]) => [`source/${name}`, file]));
  const bindingFiles = new Map();
  for (const [name, file] of source.files) {
    if (name.startsWith('sessionowner/bindings/node/') || name.startsWith('sessionowner/schema/v1/')) bindingFiles.set(`package/${name.slice('sessionowner/'.length)}`, file);
  }
  for (const name of ['index.mjs', 'index.d.ts', 'schema-closure.mjs']) if (!bindingFiles.has(`package/bindings/node/${name}`)) fail('binding_entry_missing');
  const { scripts: _scripts, ...metadata } = source.metadata;
  metadata.exports = { '.': { types: './bindings/node/index.d.ts', default: './bindings/node/index.mjs' } };
  bindingFiles.set('package/package.json', { bytes: json(metadata), mode: 0o644 });
  bindingFiles.set('package/LICENSE', source.files.get('LICENSE'));
  return { moduleFiles, bindingFiles };
}

function manifest(source, version, artifacts, bindingFiles) {
  return {
    format: 'sessionowner-release/1', repository: REPOSITORY, commit: source.commit,
    component: 'sessionowner', componentTree: source.tree,
    module: { path: MODULE, version: `v${version}`, tag: `sessionowner/v${version}` },
    binding: { name: source.metadata.name, version: source.metadata.version, private: true, registryPublish: false, packageJsonSha256: sha256(bindingFiles.get('package/package.json').bytes) },
    abi: { manifestVersion: source.abi.manifestVersion, schemaVersion: source.abi.schemaVersion, publicProtocol: source.abi.publicProtocol, privateProtocol: source.abi.privateProtocol, manifestSha256: sha256(source.schemaBytes), files: source.abi.files },
    files: [...source.files].map(([name, file]) => ({ path: name, gitBlob: file.blob, mode: file.mode.toString(8), bytes: file.bytes.length, sha256: sha256(file.bytes) })),
    artifacts: [...artifacts].map(([name, file]) => ({ file: name, bytes: file.bytes.length, sha256: sha256(file.bytes) })),
  };
}

function checksums(files) {
  return Buffer.from([...files].sort(([a], [b]) => a.localeCompare(b, 'en')).map(([name, file]) => `${sha256(file.bytes)}  ${name}\n`).join(''));
}

function compareArchive(actual, expected) {
  assert.deepEqual([...actual.keys()].sort(), [...expected.keys()].sort(), 'archive file inventory mismatch');
  for (const [name, file] of expected) {
    same(actual.get(name).bytes, file.bytes, 'archive_content_mismatch');
    if (actual.get(name).mode !== file.mode) fail('archive_mode_mismatch');
  }
}

function verify(bytes, source, version) {
  const envelope = readArchive(bytes);
  assert.deepEqual([...envelope.keys()].sort(), ['SHA256SUMS', 'manifest.json', 'private-node.tgz', 'source.tar.gz']);
  if ([...envelope.values()].some(file => file.mode !== 0o644)) fail('envelope_mode_mismatch');
  const { moduleFiles, bindingFiles } = layouts(source);
  compareArchive(readArchive(envelope.get('source.tar.gz').bytes), moduleFiles);
  compareArchive(readArchive(envelope.get('private-node.tgz').bytes), bindingFiles);
  const artifacts = new Map(['source.tar.gz', 'private-node.tgz'].map(name => [name, envelope.get(name)]));
  const expected = manifest(source, version, artifacts, bindingFiles);
  same(envelope.get('manifest.json').bytes, json(expected), 'manifest_identity_mismatch');
  const covered = new Map([...artifacts, ['manifest.json', envelope.get('manifest.json')]]);
  same(envelope.get('SHA256SUMS').bytes, checksums(covered), 'envelope_checksum_mismatch');
  return expected;
}

export function verifyRelease({ repo = process.cwd(), commit, version, bytes }) {
  return verify(bytes, snapshot(repo, commit), versionOf(version));
}

export function packageRelease({ repo = process.cwd(), commit, version, output = process.env.RELEASE_ASSET_PATH }) {
  version = versionOf(version);
  const source = snapshot(repo, commit);
  if (typeof output !== 'string' || !path.isAbsolute(output) || !/^[A-Za-z0-9][A-Za-z0-9_.-]*\.tar\.gz$/.test(path.basename(output))) fail('invalid_output_path');
  const parent = realpathSync(path.dirname(output));
  if (!lstatSync(parent).isDirectory() || parent === source.repo || parent.startsWith(source.repo + path.sep)) fail('output_must_be_outside_checkout');
  const destination = path.join(parent, path.basename(output));
  const { moduleFiles, bindingFiles } = layouts(source);
  const artifacts = new Map([
    ['source.tar.gz', { bytes: archive(moduleFiles, source.timestamp), mode: 0o644 }],
    ['private-node.tgz', { bytes: archive(bindingFiles, source.timestamp), mode: 0o644 }],
  ]);
  artifacts.set('manifest.json', { bytes: json(manifest(source, version, artifacts, bindingFiles)), mode: 0o644 });
  artifacts.set('SHA256SUMS', { bytes: checksums(artifacts), mode: 0o644 });
  const bytes = archive(artifacts, source.timestamp);
  const verified = verify(bytes, source, version);
  writeFileSync(destination, bytes, { flag: 'wx', mode: 0o600 });
  return { output: destination, bytes: bytes.length, sha256: sha256(bytes), manifest: verified };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2);
    if (args.length !== 4 || args[0] !== '--version' || args[2] !== '--commit') fail('usage: package.mjs --version SEMVER --commit FULL_SHA; RELEASE_ASSET_PATH must name the output');
    const result = packageRelease({ version: args[1], commit: args[3] });
    console.log(JSON.stringify({ output: result.output, bytes: result.bytes, sha256: result.sha256, commit: result.manifest.commit }));
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
