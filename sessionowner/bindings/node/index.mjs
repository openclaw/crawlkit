import { readFileSync } from 'node:fs';
import { createHash, randomUUID } from 'node:crypto';
import { qualifyProvider } from './schema-closure.mjs';

export const PROTOCOL = 'sessionowner/1';
export const HARD_FRAME_LIMIT = 67108864;
const prefix = 'urn:openclaw:sessionowner:abi-v1:';
const failure = (code = 'protocol_mismatch') => Object.assign(new Error(code), { code });
const hash = bytes => createHash('sha256').update(bytes).digest('hex');

// No JSON.parse duplicate-key loss: tokenize first with bounded recursion/nodes.
export function decodeJSON(bytes, cap) {
  if (!Number.isSafeInteger(cap) || cap < 1 || cap > HARD_FRAME_LIMIT || bytes.length > cap) throw failure('payload_too_large');
  let s;
  try { s = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes); } catch { throw failure(); }
  if (s.charCodeAt(0) === 0xfeff) throw failure();
  let i = 0, nodes = 0;
  const ws = () => { while (' \t\r\n'.includes(s[i]) && i < s.length) i++; };
  const str = () => {
    const start = i++;
    while (i < s.length) {
      if (s[i] === '\\') { i += 2; continue; }
      if (s[i++] === '"') {
        let value; try { value = JSON.parse(s.slice(start, i)); } catch { throw failure(); }
        for (let n = 0; n < value.length; n++) {
          const c = value.charCodeAt(n);
          if (c >= 0xdc00 && c <= 0xdfff) throw failure();
          if (c >= 0xd800 && c <= 0xdbff) { const d = value.charCodeAt(++n); if (!(d >= 0xdc00 && d <= 0xdfff)) throw failure(); }
        }
        return value;
      }
    }
    throw failure();
  };
  const value = depth => {
    if (++nodes > 1000000 || depth > 64) throw failure('payload_too_large');
    ws();
    if (s[i] === '"') return str();
    if (s[i] === '{') {
      i++; ws(); const result = Object.create(null);
      if (s[i] === '}') { i++; return result; }
      for (;;) {
        ws(); if (s[i] !== '"') throw failure(); const key = str();
        if (Object.hasOwn(result, key)) throw failure(); ws(); if (s[i++] !== ':') throw failure();
        result[key] = value(depth + 1); ws(); const end = s[i++];
        if (end === '}') return result; if (end !== ',') throw failure();
      }
    }
    if (s[i] === '[') {
      i++; ws(); const result = []; if (s[i] === ']') { i++; return result; }
      for (;;) { result.push(value(depth + 1)); ws(); const end = s[i++]; if (end === ']') return result; if (end !== ',') throw failure(); }
    }
    for (const [token, result] of [['true', true], ['false', false], ['null', null]]) if (s.startsWith(token, i)) { i += token.length; return result; }
    const n = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(s.slice(i));
    if (!n) throw failure(); i += n[0].length;
    const result = Number(n[0]); if (!Number.isFinite(result) || Number.isInteger(result) && !Number.isSafeInteger(result)) throw failure(); return result;
  };
  const result = value(0); ws(); if (i !== s.length) throw failure(); return result;
}

const isObject = v => v !== null && typeof v === 'object' && !Array.isArray(v);
const equal = (a, b) => canonical(a) === canonical(b);
function canonical(v) { if (Array.isArray(v)) return '[' + v.map(canonical).join(',') + ']'; if (isObject(v)) return '{' + Object.keys(v).sort().map(k => JSON.stringify(k) + ':' + canonical(v[k])).join(',') + '}'; return JSON.stringify(v); }
const timestamp = v => { const d = new Date(v); return typeof v === 'string' && v.length === 24 && d.getUTCFullYear() >= 1 && Number.isFinite(d.getTime()) && d.toISOString() === v; };
const keywords = new Set('$schema $id $defs $comment type properties required additionalProperties oneOf anyOf allOf not if then else enum const minLength maxLength pattern format minimum maximum minItems maxItems uniqueItems items minProperties maxProperties $ref'.split(' '));

export class SchemaStore {
  #docs = new Map();
  constructor() {
    for (const name of ['request', 'response', 'cancel', 'adapter', 'registry-policy']) {
      const b = readFileSync(new URL(`../../schema/v1/${name}.schema.json`, import.meta.url));
      this.#docs.set(prefix + name, decodeJSON(b, HARD_FRAME_LIMIT));
    }
    for (const doc of this.#docs.values()) this.#compile(doc, false);
  }
  #resolve(ref) {
    const [id, frag] = ref.split('#'); let doc = this.#docs.get(id); if (!doc) throw failure();
    if (frag !== undefined) { if (!frag.startsWith('/')) throw failure(); for (const k of frag.slice(1).split('/')) { doc = doc[k.replaceAll('~1', '/').replaceAll('~0', '~')]; if (doc === undefined) throw failure(); } }
    return doc;
  }
  #compile(s, closed, depth = 0) {
    if (depth > 64) throw failure(); if (typeof s === 'boolean') return; if (!isObject(s)) throw failure();
    for (const key of Object.keys(s)) if (!keywords.has(key)) throw failure();
    for (const key of ['$schema','$id','$comment','$ref','pattern','format']) if (s[key] !== undefined && typeof s[key] !== 'string') throw failure();
    if (s.required !== undefined && (!Array.isArray(s.required) || s.required.some(x => typeof x !== 'string') || new Set(s.required).size !== s.required.length)) throw failure();
    if (s.enum !== undefined && (!Array.isArray(s.enum) || !s.enum.length)) throw failure();
    if (s.type !== undefined && (![s.type].flat().length || [s.type].flat().some(x => !['object','array','string','boolean','null','number','integer'].includes(x)))) throw failure();
    for (const key of ['minLength','maxLength','minItems','maxItems','minProperties','maxProperties','minimum','maximum']) if (s[key] !== undefined && (typeof s[key] !== 'number' || !Number.isFinite(s[key]) || !['minimum','maximum'].includes(key) && (!Number.isInteger(s[key]) || s[key] < 0))) throw failure();
    if (s.uniqueItems !== undefined && typeof s.uniqueItems !== 'boolean') throw failure();

    if (s.$ref) { const target = this.#resolve(s.$ref); if (closed) this.#compile(target, true, depth + 1); }
    if (s.pattern) { if (s.pattern.length > 512 || /\(\?[=!<]|\\[1-9]/.test(s.pattern)) throw failure(); try { new RegExp(s.pattern, 'u'); } catch { throw failure(); } }
    if (s.format !== undefined && s.format !== 'date-time') throw failure();
    if (closed && (s.type === 'object' || s.properties !== undefined) && (s.additionalProperties === undefined || s.additionalProperties === true)) throw failure();
    for (const key of ['properties', '$defs']) if (s[key] !== undefined) { if (!isObject(s[key])) throw failure(); for (const v of Object.values(s[key])) this.#compile(v, closed, depth + 1); }
    for (const key of ['oneOf', 'anyOf', 'allOf']) if (s[key] !== undefined) { if (!Array.isArray(s[key]) || !s[key].length) throw failure(); for (const v of s[key]) this.#compile(v, closed, depth + 1); }
    for (const key of ['items', 'not', 'if', 'then', 'else', 'additionalProperties']) if (s[key] !== undefined) this.#compile(s[key], closed, depth + 1);
  }
  register(id, bytes) { if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/.test(id) || this.#docs.has(id)) throw failure(); const s = decodeJSON(bytes, HARD_FRAME_LIMIT); this.#compile(s, false); qualifyProvider(s, ref => this.#resolve(ref)); this.#docs.set(id, s); }
  validate(id, value) { if (!this.#match(this.#resolve(id), value, 0)) throw failure(); return value; }
  #match(s, v, depth) {
    if (depth > 256) return false; if (typeof s === 'boolean') return s;
    if (s.$ref && !this.#match(this.#resolve(s.$ref), v, depth + 1)) return false;
    if (Object.hasOwn(s, 'const') && !equal(s.const, v)) return false;
    if (s.enum && !s.enum.some(e => equal(e, v))) return false;
    for (const key of ['oneOf', 'anyOf', 'allOf']) if (s[key]) { const n = s[key].filter(x => this.#match(x, v, depth + 1)).length; if (key === 'oneOf' && n !== 1 || key === 'anyOf' && n === 0 || key === 'allOf' && n !== s[key].length) return false; }
    if (s.not && this.#match(s.not, v, depth + 1)) return false;
    if (Object.hasOwn(s, 'if')) { const key = this.#match(s.if, v, depth + 1) ? 'then' : 'else'; if (Object.hasOwn(s, key) && !this.#match(s[key], v, depth + 1)) return false; }
    const types = { object: isObject(v), array: Array.isArray(v), string: typeof v === 'string', boolean: typeof v === 'boolean', null: v === null, number: typeof v === 'number', integer: Number.isSafeInteger(v) };
    if (s.type && ![s.type].flat().some(t => types[t])) return false;
    const bounds = (n, lo, hi) => (s[lo] === undefined || n >= s[lo]) && (s[hi] === undefined || n <= s[hi]);
    if (typeof v === 'string') {
      let length = 0; for (const _ of v) length++;
      if (!bounds(length, 'minLength', 'maxLength') || s.pattern && !new RegExp(s.pattern, 'u').test(v) || s.format === 'date-time' && !timestamp(v)) return false;
      if (s.pattern === '^(0|[1-9][0-9]{0,19})$' && BigInt(v) > 18446744073709551615n) return false;
    }
    if (typeof v === 'number' && !bounds(v, 'minimum', 'maximum')) return false;
    if (Array.isArray(v)) { if (!bounds(v.length, 'minItems', 'maxItems') || s.items !== undefined && v.some(x => !this.#match(s.items, x, depth + 1))) return false; if (s.uniqueItems && new Set(v.map(canonical)).size !== v.length) return false; }
    if (isObject(v)) {
      if (!bounds(Object.keys(v).length, 'minProperties', 'maxProperties') || s.required?.some(k => !Object.hasOwn(v, k))) return false;
      for (const [k, x] of Object.entries(v)) { const sub = s.properties?.[k] ?? s.additionalProperties; if (sub !== undefined && !this.#match(sub, x, depth + 1)) return false; }
    }
    return true;
  }
}

export class Registry {
  #policy; #schemas; #operations = new Map(); #builtin = new Map();
  constructor(policyBytes, expectedHash, artifacts) {
    if (hash(policyBytes) !== expectedHash) throw failure(); this.#schemas = new SchemaStore();
    this.#policy = this.#schemas.validate(prefix + 'registry-policy', decodeJSON(policyBytes, HARD_FRAME_LIMIT));
    const slots = new Map(), adapters = new Map();
    for (const s of this.#policy.schemas) { const bytes = artifacts.get(s.schemaId); if (!bytes || slots.has(s.schemaId) || hash(bytes) !== s.sha256) throw failure(); this.#schemas.register(s.schemaId, bytes); slots.set(s.schemaId, s.slot); }
    for (const a of this.#policy.adapters) {
      if (adapters.has(a.adapterId) || slots.get(a.privateContextSchemaId) !== 'private_context' || slots.get(a.checkpointEvidenceSchemaId) !== 'checkpoint_evidence') throw failure();
      for (const [slot, key] of [['candidate', 'candidateSchemaId'], ['credential_delta', 'credentialDeltaSchemaId'], ['proof', 'proofSchemaId']]) if (a[key] !== null && slots.get(a[key]) !== slot) throw failure(); adapters.set(a.adapterId, a);
    }
    for (const op of this.#policy.operations) {
      const key = `${op.operationId}/${op.operationVersion}/${op.requestKind}`; if (this.#operations.has(key) || op.adapterId !== null && !adapters.has(op.adapterId)) throw failure();
      for (const [slot, key] of [['target', 'targetSchemaId'], ['args', 'argsSchemaId'], ['result', 'resultSchemaId']]) if (op[key] !== null && slots.get(op[key]) !== slot) throw failure();
      if (new Set(op.budgetPlans.map(p => p.planId)).size !== op.budgetPlans.length) throw failure();
      this.#operations.set(key, op); if (op.requestKind !== 'perform') { if (this.#builtin.has(op.requestKind)) throw failure(); this.#builtin.set(op.requestKind, op); }
    }
  }
  get bindingId() { return this.#policy.bindingId; }
  get channelCap() { return Math.max(...this.#policy.operations.map(o => o.wireCaps.requestFrameBytes)); }
  parseRequest(bytes) {
    const req = decodeJSON(bytes, this.channelCap); this.#schemas.validate(prefix + (req.kind === 'cancel' ? 'cancel' : 'request'), req);
    if (req.bindingId !== this.bindingId) throw failure('permission_denied');
    const op = req.kind === 'perform' ? this.#operations.get(`${req.operationId}/${req.operationVersion}/perform`) : this.#builtin.get(req.kind === 'recover' ? `recover_${req.mode}` : req.kind);
    if (!op) throw failure('unsupported_operation'); if (bytes.length > op.wireCaps.requestFrameBytes) throw failure('payload_too_large');
    if (req.kind === 'perform') { this.#schemas.validate(op.targetSchemaId, req.target); this.#schemas.validate(op.argsSchemaId, req.args); if (op.confirmationRequired !== (req.confirmationRef !== null)) throw failure('permission_denied'); }
    return { request: req, operation: structuredClone(op) };
  }
  validateResponse(bytes, pending) {
    const { request: req, operation: op } = pending;
    const res = this.#schemas.validate(prefix + 'response', decodeJSON(bytes, op.wireCaps.responseFrameBytes));
    if (res.requestId !== req.requestId || res.bindingId !== req.bindingId) throw failure();
    if (res.kind === 'performed') { if (req.kind !== 'perform' || res.operationId !== req.operationId || res.operationVersion !== req.operationVersion) throw failure(); this.#schemas.validate(op.resultSchemaId, res.result); }
    else if (res.kind === 'failed') { if (res.requestKind !== req.kind || ['certainty', 'nextAction', 'retryNotBefore'].some(k => !equal(res[k], res.failure[k])) || res.failure.detailCode !== null && !this.#policy.detailCodes.includes(res.failure.detailCode)) throw failure(); }
    else if (res.kind === 'inspection') { if (req.kind !== 'inspect' || res.state === 'unenrolled' && [res.generation, res.stateRevision, res.safeIdentity].some(x => x !== null) || res.state === 'ready' && [res.generation, res.stateRevision, res.safeIdentity].some(x => x === null)) throw failure(); }
    else if (res.kind === 'recovered') { if (req.kind !== 'recover') throw failure(); }
    else if (res.kind === 'cancel_ack') { if (req.kind !== 'cancel' || res.targetRequestId !== req.targetRequestId) throw failure(); }
    else throw failure(); return res;
  }
}

export function encodeFrame(value, cap) {
  const bytes = ArrayBuffer.isView(value) ? Buffer.from(value.buffer, value.byteOffset, value.byteLength) : Buffer.from(JSON.stringify(value));
  if (cap < 1 || cap > HARD_FRAME_LIMIT || !bytes.length || bytes.length > cap) throw failure('payload_too_large');
  const header = Buffer.alloc(4); header.writeUInt32BE(bytes.length); return Buffer.concat([header, bytes]);
}
export class FrameDecoder {
  #cap; #buffer = Buffer.alloc(0);
  constructor(cap) { if (!Number.isSafeInteger(cap) || cap < 1 || cap > HARD_FRAME_LIMIT) throw failure(); this.#cap = cap; }
  push(chunk) {
    const frames = []; let offset = 0;
    while (offset < chunk.length) {
      let needed = 4;
      if (this.#buffer.length >= 4) { const n=this.#buffer.readUInt32BE(); if (!n || n>this.#cap) throw failure('payload_too_large'); needed=n+4; }
      const take=Math.min(needed-this.#buffer.length, chunk.length-offset);
      this.#buffer=Buffer.concat([this.#buffer,chunk.subarray(offset,offset+take)]); offset+=take;
      if(this.#buffer.length>=4) { const n=this.#buffer.readUInt32BE(); if (!n || n>this.#cap) throw failure('payload_too_large'); if(this.#buffer.length===n+4) {frames.push(this.#buffer.subarray(4));this.#buffer=Buffer.alloc(0);} }
    }
    return frames;
  }
  end() { if (this.#buffer.length) throw failure(); }
}

// Transport is injected by the authenticated host. No shell, listener, SDK,
// credential paths or session state is opened by this thin client.
export class OperationClient {
  #transport; #registry;
  constructor(registry, authenticatedTransport) { this.#registry = registry; this.#transport = authenticatedTransport; }
  async request(request, options = {}) {
    const raw = Buffer.from(JSON.stringify(request)), pending = this.#registry.parseRequest(raw);
    const reply = await this.#transport.roundTrip(encodeFrame(raw, pending.operation.wireCaps.requestFrameBytes), { signal: options.signal });
    const decoder = new FrameDecoder(pending.operation.wireCaps.responseFrameBytes); const frames = decoder.push(reply); decoder.end(); if (frames.length !== 1) throw failure();
    return this.#registry.validateResponse(frames[0], pending);
  }
  cancel(targetRequestId, deadline) { return this.request({ protocol: PROTOCOL, kind: 'cancel', requestId: randomUUID(), bindingId: this.#registry.bindingId, deadline, targetRequestId, reason: 'caller_request' }); }
}
