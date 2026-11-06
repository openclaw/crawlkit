import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { Registry, SchemaStore, decodeJSON, encodeFrame, FrameDecoder, HARD_FRAME_LIMIT, OperationClient } from './index.mjs';

const root = new URL('../../conformance/', import.meta.url);
const policy = readFileSync(new URL('registry.json', root));
const artifacts = new Map(['target','args','result','context','candidate','delta','proof','evidence'].map(n => [n, readFileSync(new URL(`${n}.schema.json`,root))]));
const registry = () => new Registry(policy, createHash('sha256').update(policy).digest('hex'), artifacts);
const fixture = () => ({protocol:'sessionowner/1',kind:'perform',requestId:'request-1',bindingId:'fixture-binding',deadline:'2099-01-02T03:04:05.000Z',expectedGeneration:'epoch-1',operationId:'fixture.read',operationVersion:1,target:{id:'target-1'},args:{cursor:''},confirmationRef:null});
const response = value => ({protocol:'sessionowner/1',kind:'performed',requestId:'request-1',bindingId:'fixture-binding',acceptedGeneration:'epoch-1',finalGeneration:'epoch-1',stateRevision:'1',receiptRef:'receipt-1',certainty:'completed',nextAction:'none',retryNotBefore:null,operationId:'fixture.read',operationVersion:1,result:{value}});
const bytes = value => Buffer.from(JSON.stringify(value));

test('same frozen Go/Node valid-invalid corpus', () => {
  const r = registry();
  for (const c of JSON.parse(readFileSync(new URL('corpus.json',root)))) {
    const raw = c.wireHex ? Buffer.from(c.wireHex,'hex') : Buffer.from(c.wire);
    const run = () => c.plane === 'json' ? decodeJSON(raw,HARD_FRAME_LIMIT) : c.plane === 'request' ? r.parseRequest(raw) : r.validateResponse(raw,r.parseRequest(Buffer.from(c.request)));
    if(c.want) assert.doesNotThrow(run,c.name); else assert.throws(run,c.name);
  }
});
test('exact registered cursor and worst-case native body parity, never truncated', () => {
  const r=registry(), request=fixture(); request.args.cursor='\x01'.repeat(87423);
  assert.doesNotThrow(()=>r.parseRequest(bytes(request))); request.args.cursor='x'.repeat(87424);assert.throws(()=>r.parseRequest(bytes(request)));
  const pending=r.parseRequest(bytes(fixture()));const raw=bytes(response('\x01'.repeat(8388608)));
  assert.ok(raw.length>50331648);assert.doesNotThrow(()=>r.validateResponse(raw,pending));assert.throws(()=>r.validateResponse(bytes(response('x'.repeat(8388609))),pending));
});
test('frame exact/+1 and split chunks', () => {
  for(const n of [8192,16384,87423,8388608]) {
    const payload=Buffer.alloc(n,120), frame=encodeFrame(payload,n), decoder=new FrameDecoder(n);
    assert.deepEqual(decoder.push(frame.subarray(0,3)),[]);const result=decoder.push(frame.subarray(3));decoder.end();assert.equal(result.length,1);assert.deepEqual(result[0],payload);assert.throws(()=>encodeFrame(Buffer.alloc(n+1),n));
  }
  assert.throws(()=>new FrameDecoder(128).push(Buffer.from([0,0,1,0])));
  const decoder=new FrameDecoder(128);decoder.push(Buffer.from([0,0]));assert.throws(()=>decoder.end());
});
test('provider schema cannot reopen payloads or import unknown keywords', () => {
  const s=new SchemaStore();assert.throws(()=>s.register('open',bytes({type:'object',properties:{}})));
  assert.throws(()=>s.register('unsupported',bytes({type:'object',additionalProperties:false,dependentSchemas:{}})));
  assert.throws(()=>s.register('ref-open',bytes({type:'object',additionalProperties:false,properties:{x:{$ref:'urn:openclaw:sessionowner:abi-v1:request#/$defs/RegisteredObject'}}})));
});
test('thin client uses supplied authenticated transport and fixed registry only', async () => {
  const r=registry();let calls=0;const client=new OperationClient(r,{async roundTrip(frame){calls++;const d=new FrameDecoder(r.channelCap);const [req]=d.push(frame);d.end();r.parseRequest(req);return encodeFrame(response('public'),67108864);}});
  assert.equal((await client.request(fixture())).result.value,'public');assert.equal(calls,1);
  await assert.rejects(client.request({...fixture(),url:'https://unregistered.invalid'}));assert.equal(calls,1);
});

test('B1 R5/R6 same Go/Node qualification and conditional corpus', () => {
  for (const c of JSON.parse(readFileSync(new URL('b1-schema-corpus.json',root)))) {
    const store=new SchemaStore();const register=()=>store.register('b1-schema',bytes(c.schema));
    if(!c.register){assert.throws(register,c.name);continue;}
    assert.doesNotThrow(register,c.name);
    for(const value of c.valid)assert.doesNotThrow(()=>store.validate('b1-schema',value),c.name);
    for(const value of c.invalid)assert.throws(()=>store.validate('b1-schema',value),c.name);
  }
});


// B2C3A: compact shared bytes/recipes only, never a private authority or SDK.
const b2c3CorpusBytes = readFileSync(new URL('b2c3-wire-corpus.json', root));
assert.ok(b2c3CorpusBytes.length <= 65536);
const b2c3Corpus = JSON.parse(b2c3CorpusBytes);
assert.equal(b2c3Corpus.schema, 'sessionowner-b2c3-wire-recipes/1');
assert.equal(b2c3Corpus.maxWireBytes, 2000001);
assert.equal(b2c3Corpus.cases.length, 30);
assert.equal(new Set(b2c3Corpus.cases.map(c => c.id)).size, 30);
let b2c3Active = false;
for (const c of b2c3Corpus.cases) {
  test(`B2C3 ${c.id} ${c.name}`, { concurrency: false }, () => {
    assert.equal(b2c3Active, false, 'only one new vector may be active');
    b2c3Active = true;
    try {
      const r = registry();
      assert.equal(r.channelCap, 1048576);
      const seed = name => {
        assert.equal(typeof b2c3Corpus.seeds[name], 'string', `missing shared seed ${name}`);
        return Buffer.from(b2c3Corpus.seeds[name], 'utf8');
      };
      const check = (variant, entry, raw, want, wantCode, run) => {
        assert.ok(raw.length <= b2c3Corpus.maxWireBytes);
        let value, accepted = false, code = '';
        try { value = run(raw); accepted = true; }
        catch (error) { code = typeof error.code === 'string' ? error.code : 'non_core_error'; }
        console.log('B2C3_WIRE ' + JSON.stringify({ caseId: c.id, variant, entry,
          bytes: raw.length, sha256: createHash('sha256').update(raw).digest('hex'), accepted, code,
          expectedAccepted: want, expectedCode: wantCode }));
        assert.equal(accepted, want, `${c.id}/${variant}/${entry} acceptance`);
        assert.equal(code, wantCode, `${c.id}/${variant}/${entry} safe code`);
        return value;
      };
      const parse = raw => r.parseRequest(raw);
      const decode = raw => decodeJSON(raw, HARD_FRAME_LIMIT);
      const pad = (raw, size) => {
        assert.ok(size >= raw.length);
        return Buffer.concat([raw, Buffer.alloc(size - raw.length, 32)]);
      };
      switch (c.family) {
        case 'request-cap': {
          let raw = seed(c.seed);
          const pending = check('shape', 'request', raw, true, '', parse);
          assert.equal(pending.operation.wireCaps.requestFrameBytes, 8192);
          assert.equal(pending.operation.requestKind, c.seed.replaceAll('-', '_'));
          raw = pad(raw, c.size);
          assert.equal(raw.length, c.size); assert.ok(c.size < r.channelCap);
          check('main', 'request', raw, c.accept, c.code, parse);
          break;
        }
        case 'response-cap':
        case 'revision': {
          const pending = check('pending', 'request', seed(c.pending), true, '', parse);
          const validate = raw => r.validateResponse(raw, pending);
          let raw = seed(c.seed);
          check('shape', 'response', raw, true, '', validate);
          if (c.family === 'response-cap') {
            assert.equal(pending.operation.wireCaps.responseFrameBytes, 16384);
            raw = pad(raw, c.size); assert.equal(raw.length, c.size);
          } else {
            raw = seed(c.negative); assert.ok(raw.includes(Buffer.from('"stateRevision":"00"')));
          }
          check('main', 'response', raw, c.accept, c.code, validate);
          break;
        }
        case 'private': {
          const store = new SchemaStore();
          const privateGrammar = raw => store.validate('urn:openclaw:sessionowner:abi-v1:adapter', decode(raw));
          let raw = seed(c.seed);
          check('main', 'private', raw, true, '', privateGrammar);
          check('main', 'public-request', raw, false, 'protocol_mismatch', parse);
          raw = seed(c.negative);
          assert.ok(b2c3Corpus.seeds[c.seed] !== raw.toString('utf8'), 'private structural mutation must differ');
          assert.ok(c.negativeDescription.length > 0);
          check('negative', 'private', raw, false, 'protocol_mismatch', privateGrammar);
          check('negative', 'public-request', raw, false, 'protocol_mismatch', parse);
          break;
        }
        case 'id': {
          const original = seed(c.seed).toString('utf8'), token = '"requestId":"r"';
          assert.equal(original.split(token).length - 1, 1);
          const raw = Buffer.from(original.replace(token, '"requestId":"' + 'a'.repeat(c.count) + '"'));
          check('main', 'request', raw, c.accept, c.code, parse);
          break;
        }
        case 'scalar': {
          assert.ok(['9007199254740991', '9007199254740992'].includes(c.literal));
          check('main', 'json', Buffer.from(c.literal), c.accept, c.code, decode);
          break;
        }
        case 'depth': {
          const raw = Buffer.from('['.repeat(c.count) + '0' + ']'.repeat(c.count));
          assert.equal(raw.length, 2 * c.count + 1);
          assert.equal(raw.reduce((n, b) => n + Number(b === 91), 0), c.count);
          check('main', 'json', raw, c.accept, c.code, decode);
          break;
        }
        case 'nodes': {
          assert.ok([999999, 1000000].includes(c.count));
          const raw = Buffer.from('[' + '0,'.repeat(c.count - 1) + '0]');
          assert.equal(raw.length, 2 * c.count + 1);
          assert.equal(raw.reduce((n, b) => n + Number(b === 48), 0), c.count);
          check('main', 'json', raw, c.accept, c.code, wire => {
            const value = decode(wire);
            assert.ok(Array.isArray(value)); assert.equal(value.length, c.count);
            return value;
          });
          break;
        }
        default: assert.fail(`unrecognized shared recipe family ${c.family}`);
      }
    } finally { b2c3Active = false; }
  });
}
