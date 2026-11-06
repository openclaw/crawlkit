# B2C3A: shared Go/Node wire recipes

This is a test, compact corpus and documentation slice only. All production Go,
JavaScript, TypeScript, exported APIs, package/dependency manifests, frozen ABI,
old corpus/registry inputs, FD3 fixtures and C2 implementation remain unchanged.
No parser fix, new Node authority, SDK binding or provider behavior is introduced.
C2's current-source independent review is separate and is not accepted by this work.

## Exact admitted cases

The new `conformance/b2c3-wire-corpus.json` contains 30 named recipes, A01-A30,
from the verified C3 slice-A table. `wire_conformance_test.go` and the appended
portion of `bindings/node/index.test.mjs` consume the same exact raw JSON seed
strings. They do not independently serialize conceptual seed objects and assume
byte equivalence. Padding, ID replacement, nested arrays and zero-element arrays
use specified ASCII byte operations in both drivers.

| Cases | Boundary and actual entrypoint |
| --- | --- |
| A01-A02 | Inspect request, 8192 bytes accepted and 8193 refused |
| A03-A04 | Cancel request, the same exact/plus-one request cap |
| A05-A06 | Recover-new request, the same exact/plus-one request cap |
| A07-A08 | Recover-resume request, the same exact/plus-one request cap |
| A09-A10 | Inspection response, 16384 bytes accepted and 16385 refused |
| A11-A12 | Cancel-ack response, the same exact/plus-one response cap |
| A13-A14 | Recovered response, the same exact/plus-one response cap |
| A15-A20 | Private invoke, checkpoint, checkpoint-ack, adapter-result, adapter-cancel and adapter-cancel-ack grammar |
| A21 | Private invoke with the staged candidate union instead of inline |
| A22-A23 | Request ID with 128 allowed ASCII characters accepted, 129 refused |
| A24 | Noncanonical revision `00` refused against a valid performed-response control |
| A25-A26 | Raw JSON integer 9007199254740991 accepted, 9007199254740992 refused |
| A27-A28 | Raw JSON with 64 nested arrays accepted, 65 refused |
| A29-A30 | Raw JSON with exactly 1,000,000 value nodes accepted, one more refused |

Request caps use `Registry.ParseRequest` and `Registry.parseRequest`, not raw frame
buffers. Each first proves its unpadded shape and registered kind-specific 8192-byte
cap under the unchanged 1,048,576-byte channel cap. Legal trailing whitespace creates
the exact wire size; the plus-one error must be `payload_too_large` at the operation
cap, not a schema or channel rejection.

Response caps use matching pending requests and the actual response validators.
The unpadded response must validate with its original correlation before padding.
The 16384-byte registered cap is asserted, and the overflow must fail with the
fixed size error. The performed response used for A24 retains a separate valid
control and matching request; existing uint64 maximum/overflow cases are not
reintroduced or counted as new.

## Private grammar, not private authority

The existing Node `SchemaStore` already loads the frozen adapter schema and offers
generic validation. The new tests use that interface and Go's existing schema
store. They do not implement an AdapterSession, durable ACK, writer, lease issuer,
candidate repository or SDK transport in Node.

Each A15-A21 leaf contains one positive private wire, public-request rejection of
that same wire, one explicit structural negative and public rejection of the
negative. The seven negatives respectively remove required budget, reverse
checkpoint direction, remove durable-state revision, remove result certainty,
use an unknown cancel reason, remove cancel disposition and mix forbidden inline
value data into a staged candidate. All seeds retain independently specified lease
fields, required nulls and correct directions for their positive grammar variant.
There are six top-level private kinds plus one nested staged variant, not seven
kinds or qualification of every runtime lease/state mutation.

The staged representation remains exactly `{mode,candidateRef,bytes,privateDigest}`;
the inline representation remains `{mode,value}`. No TTL, expiry, file access or
candidate material custody is inferred from grammar acceptance.

## Raw limits and resource discipline

A25-A30 call the actual `DecodeJSON`/`decodeJSON` primitives with the existing hard
frame ceiling. They cannot pass merely because an operation schema rejected a
scalar, an oversized ID or an unrelated object. Go also checks the fixed size
refusal detail for structure versus request/response caps.

The node-count vectors contain 999,999 or 1,000,000 zero elements plus their root
array. Their actual byte lengths are 1,999,999 and 2,000,001, respectively. The
constructed zero count and successful decoded array length are asserted. Depth
vectors similarly assert their bracket count and actual byte length. No huge
literal or decoded payload is committed or logged.

The corpus is 21,619 bytes, below its 65,536-byte bound. One new vector is active
at a time; all new wire bytes are at most 2,000,001. The drivers are sequential and
record only case/variant/entrypoint, actual wire SHA-256, UTF-8 byte count, actual
acceptance/code and explicit expected acceptance/code.

## Evidence and accounting

The 30 leaves per language contain 73 validation records over 59 named wire
variants, with 46 distinct wire hashes after repeated controls are deduplicated.
The counts are deliberately separate: the same private bytes are checked on two
planes, and cap cases retain unpadded and pending-request controls. These are not
73 new conformance requirements or 59 new leaves.

Initial new-only Go and Node gates passed all 30 cases with matching actual wire
hashes, lengths and results. The initial driver inputs are retained separately;
final proof logging adds explicit expectation fields without changing recipes,
wire bytes, assertions or leaf counts. Final-source proof comes from the required
Go namespace gate and affected Node test entry, not an extra final focused Go run.
The existing six Node regression tests run only as part of the affected final
entry and are reported separately from the 30 new tests.

Node execution uses the existing `/opt/homebrew/opt/node@24/bin/node`, verified as
v24.21.0 with a recorded binary hash and inspected local/std-library dependency
graph. An initial conservative preflight stopped on nonempty startup settings;
inspection identified inherited CA trust selections, not executable preloads.
They were preserved unchanged. `NODE_OPTIONS`, module-path and dynamic-library
preload settings were absent. No certificate contents, SDK, provider, network,
Keychain API or configuration write is part of the test graph; no alternate binary,
startup-option clearing, install or containment workaround was used.

The exact source, recipes, initial/final Go and Node records, parity comparison,
Node startup receipt, gates, documentation and custody evidence live under
`c1b2c3a` in the parent implementation evidence root. No production or fixture
contract failure is silently weakened. A genuine parser/schema failure requires a
separate source-fix admission rather than a change to this test-only slice.

## Remaining qualification

This finite extension covers selected C01/C31 and ABI03/08/15/16/19/20 subclaims,
not full 62-requirement conformance. Family B's ordinary queue, isolation and phase
cases are not part of this slice. All-private semantic/lease permutations, native
backends, provider identity/cursors, SDK/browser/OS qualification, independent
review, distribution and consumer acceptance remain separate.

[B2C2.md](B2C2.md) retains its independently owned settlement review status;
[B3.md](B3.md) retains its source-bound named closures. This work grants no host
policy, publication, installation, runtime settlement or consumer activation.


## Later bounded A review disposition

The parent accepted the carried 30-oracle/73-parity inspection after a separate
closure-only custody check, bound by combined closure
`06b3033ca31afe91debb69c7979f01a97c4a780ae5e30718f465510defcdea51`.
No new tests or semantic audit were run for that closeout. The original report's
127.891s report-target and 67.891s hard-cutoff lateness remain unchanged; the fresh
closeout is not retrospective timing compliance or whole-row acceptance.
