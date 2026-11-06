package sessionowner

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type b2c3WireCase struct {
	ID, Name, Family, Seed, Pending, Negative, NegativeDescription, Literal, Code string
	Size, Count                                                                   int
	Accept                                                                        bool
}
type b2c3WireCorpus struct {
	Schema       string
	MaxWireBytes int
	Seeds        map[string]string
	Cases        []b2c3WireCase
}

type b2c3WireRecord struct {
	CaseID           string `json:"caseId"`
	Variant          string `json:"variant"`
	Entry            string `json:"entry"`
	Bytes            int    `json:"bytes"`
	SHA256           string `json:"sha256"`
	Accepted         bool   `json:"accepted"`
	Code             string `json:"code"`
	ExpectedAccepted bool   `json:"expectedAccepted"`
	ExpectedCode     string `json:"expectedCode"`
}

func TestB2C3WireConformance(t *testing.T) {
	encoded, err := os.ReadFile("conformance/b2c3-wire-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 65536 {
		t.Fatal("recipe corpus exceeded admitted compact bound")
	}
	var corpus b2c3WireCorpus
	if err = json.Unmarshal(encoded, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Schema != "sessionowner-b2c3-wire-recipes/1" || corpus.MaxWireBytes != 2000001 || len(corpus.Cases) != 30 {
		t.Fatal("wrong corpus contract")
	}
	r := fixtureRegistry(t)
	if r.ChannelCap() != 1048576 {
		t.Fatal("unexpected frozen channel cap")
	}
	seen := map[string]bool{}
	for _, tc := range corpus.Cases {
		if seen[tc.ID] {
			t.Fatal("duplicate case ID")
		}
		seen[tc.ID] = true
		t.Run(tc.ID+"_"+tc.Name, func(t *testing.T) {
			seed := func(name string) []byte {
				t.Helper()
				value, ok := corpus.Seeds[name]
				if !ok {
					t.Fatal("missing shared seed", name)
				}
				return []byte(value)
			}
			check := func(variant, entry string, raw []byte, want bool, wantCode string, run func([]byte) error) bool {
				t.Helper()
				if len(raw) > corpus.MaxWireBytes {
					t.Fatal("constructed wire exceeds admitted bound")
				}
				e := run(raw)
				code := ""
				if e != nil {
					code = string(ErrorCode(e))
				}
				record := b2c3WireRecord{tc.ID, variant, entry, len(raw), Hash(raw), e == nil, code, want, wantCode}
				line, _ := json.Marshal(record)
				t.Log("B2C3_WIRE " + string(line))
				if (e == nil) != want || code != wantCode {
					t.Errorf("wire contract mismatch case=%s variant=%s entry=%s accepted=%v code=%s expected=%v/%s", tc.ID, variant, entry, e == nil, code, want, wantCode)
				}
				if e != nil && wantCode == "payload_too_large" {
					fixed, ok := e.(*Error)
					if !ok {
						t.Fatal("non-core size refusal")
					}
					wantDetail := "structure"
					if tc.Family == "request-cap" {
						wantDetail = "request"
					}
					if tc.Family == "response-cap" {
						wantDetail = "response"
					}
					if fixed.Detail != wantDetail {
						t.Errorf("wrong refusal stage: got=%s want=%s", fixed.Detail, wantDetail)
					}
				}
				return e == nil
			}
			parse := func(raw []byte) error { _, e := r.ParseRequest(raw); return e }
			decode := func(raw []byte) error { _, e := DecodeJSON(raw, HardFrameLimit); return e }
			private := func(raw []byte) error {
				v, e := DecodeJSON(raw, HardFrameLimit)
				if e != nil {
					return e
				}
				return r.SchemaStore().Validate(schemaPrefix+"adapter", v)
			}
			pad := func(raw []byte, size int) []byte {
				t.Helper()
				if size < len(raw) {
					t.Fatal("padding smaller than seed")
				}
				return append(raw, bytes.Repeat([]byte(" "), size-len(raw))...)
			}
			switch tc.Family {
			case "request-cap":
				raw := seed(tc.Seed)
				var pending *Request
				if !check("shape", "request", raw, true, "", func(raw []byte) error { var e error; pending, e = r.ParseRequest(raw); return e }) {
					t.Fatal("unpadded request shape not reached")
				}
				if pending.Operation.WireCaps.RequestFrameBytes != 8192 || pending.Operation.RequestKind != strings.ReplaceAll(tc.Seed, "-", "_") {
					t.Fatal("wrong kind-specific cap")
				}
				raw = pad(raw, tc.Size)
				if len(raw) != tc.Size || tc.Size >= r.ChannelCap() {
					t.Fatal("wrong exact request bytes/channel intersection")
				}
				check("main", "request", raw, tc.Accept, tc.Code, parse)
			case "response-cap", "revision":
				pendingWire := seed(tc.Pending)
				var pending *Request
				if !check("pending", "request", pendingWire, true, "", func(raw []byte) error { var e error; pending, e = r.ParseRequest(raw); return e }) {
					t.Fatal("pending request invalid")
				}
				validate := func(raw []byte) error { return r.ValidateResponse(raw, pending) }
				raw := seed(tc.Seed)
				if !check("shape", "response", raw, true, "", validate) {
					t.Fatal("correlated response shape not reached")
				}
				if tc.Family == "response-cap" {
					if pending.Operation.WireCaps.ResponseFrameBytes != 16384 {
						t.Fatal("wrong kind-specific response cap")
					}
					raw = pad(raw, tc.Size)
					if len(raw) != tc.Size {
						t.Fatal("wrong exact response bytes")
					}
				} else {
					raw = seed(tc.Negative)
					if !bytes.Contains(raw, []byte(`"stateRevision":"00"`)) {
						t.Fatal("noncanonical revision not installed")
					}
				}
				check("main", "response", raw, tc.Accept, tc.Code, validate)
			case "private":
				// Generic frozen private grammar only. No AdapterSession, lease
				// authority, candidate repository or checkpoint effect is invoked.
				raw := seed(tc.Seed)
				check("main", "private", raw, true, "", private)
				check("main", "public-request", raw, false, "protocol_mismatch", parse)
				raw = seed(tc.Negative)
				if corpus.Seeds[tc.Seed] == string(raw) || tc.NegativeDescription == "" {
					t.Fatal("private structural mutation absent")
				}
				check("negative", "private", raw, false, "protocol_mismatch", private)
				check("negative", "public-request", raw, false, "protocol_mismatch", parse)
			case "id":
				raw := seed(tc.Seed)
				token := []byte(`"requestId":"r"`)
				if bytes.Count(raw, token) != 1 {
					t.Fatal("request ID seed token not unique")
				}
				raw = bytes.Replace(raw, token, []byte(`"requestId":"`+strings.Repeat("a", tc.Count)+`"`), 1)
				check("main", "request", raw, tc.Accept, tc.Code, parse)
			case "scalar":
				raw := []byte(tc.Literal)
				if tc.Literal != "9007199254740991" && tc.Literal != "9007199254740992" {
					t.Fatal("wrong scalar boundary")
				}
				check("main", "json", raw, tc.Accept, tc.Code, decode)
			case "depth":
				raw := []byte(strings.Repeat("[", tc.Count) + "0" + strings.Repeat("]", tc.Count))
				if len(raw) != 2*tc.Count+1 || bytes.Count(raw, []byte("[")) != tc.Count {
					t.Fatal("wrong constructed depth")
				}
				check("main", "json", raw, tc.Accept, tc.Code, decode)
			case "nodes":
				if tc.Count != 999999 && tc.Count != 1000000 {
					t.Fatal("wrong node recipe")
				}
				raw := []byte("[" + strings.Repeat("0,", tc.Count-1) + "0]")
				if len(raw) != 2*tc.Count+1 || bytes.Count(raw, []byte("0")) != tc.Count {
					t.Fatal("wrong actual node construction")
				}
				check("main", "json", raw, tc.Accept, tc.Code, func(raw []byte) error {
					v, e := DecodeJSON(raw, HardFrameLimit)
					if e == nil {
						a, ok := v.([]any)
						if !ok || len(a) != tc.Count {
							t.Fatal("decoded node count mismatch")
						}
					}
					return e
				})
			default:
				t.Fatal("unrecognized shared recipe family")
			}
		})
	}
}
