package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func seedFixture(t *testing.T) (SlackSeedManifest, [][]byte) {
	t.Helper()
	m := SlackSeedManifest{Contract: SlackSeedContract, App: "slacrawl", Archive: "fixture/archive", BindingID: "fixture-binding",
		SeedID: "0123456789abcdef0123456789abcdef", Profile: "slacrawl-private-archive-v1", NativeSchema: 9,
		NativeThrough: "slacrawl-native-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:0:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Scope:         SlackSeedScope{WorkspaceID: "TTEST", ChannelIDs: []string{"CZ", "CA"}}}
	var bodies [][]byte
	for _, name := range slackSeedTables {
		table := SeedTable{Name: name, Rows: "0", SHA256: seedDigest(nil), Chunks: []SeedChunkInfo{}}
		if name == "workspaces" || name == "messages" {
			c := SlackSeedChunk{Contract: m.Contract, App: m.App, Archive: m.Archive, BindingID: m.BindingID, SeedID: m.SeedID,
				Index: len(bodies), ExpectedCursor: seedPreviousCursor(m.SeedID, len(bodies)),
				Changes: []Change{{Table: name, Operation: "upsert", Key: []ChangeValue{{Kind: "null"}},
					Row: []ChangeValue{{Kind: "null"}, changeText(""), changeText("☃\x00tail"), changeInteger("9223372036854775807"), changeInteger("-9223372036854775808")}}}}
			// Raw seed rows retain multiplicity, even for an ambiguous NULL key.
			c.Changes = append(c.Changes, c.Changes[0])
			body, err := EncodeSlackSeedChunk(c)
			if err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, body)
			table.Rows = "2"
			table.Chunks = []SeedChunkInfo{{Rows: 2, Bytes: len(body), SHA256: seedDigest(body)}}
			table.SHA256 = seedTableHash(table.Chunks)
		}
		m.Tables = append(m.Tables, table)
	}
	return m, bodies
}

func seedTableHash(chunks []SeedChunkInfo) string {
	hash := sha256.New()
	for _, chunk := range chunks {
		digest, _ := hex.DecodeString(chunk.SHA256)
		_, _ = hash.Write(digest)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func seedPlan(t *testing.T, m SlackSeedManifest) (*SlackSeedPlan, []byte) {
	t.Helper()
	body, err := EncodeSlackSeedManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeSlackSeedManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	return p, body
}

func seedCompletion(t *testing.T, p *SlackSeedPlan) []byte {
	t.Helper()
	m := p.Manifest()
	body, err := EncodeSlackSeedComplete(SlackSeedComplete{Contract: m.Contract, App: m.App, Archive: m.Archive, BindingID: m.BindingID,
		SeedID: m.SeedID, ManifestSHA256: p.ManifestSHA256(), ExpectedCursor: seedPreviousCursor(m.SeedID, len(p.chunks))})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSlackSeedPlanH0HashOrderAndRowFidelity(t *testing.T) {
	m, chunks := seedFixture(t)
	p, body := seedPlan(t, m)
	if m.Scope.ChannelIDs[0] != "CZ" || p.Manifest().Scope.ChannelIDs[0] != "CA" {
		t.Fatal("encoder did not sort its own scope copy")
	}
	if again, err := EncodeSlackSeedManifest(m); err != nil || !bytes.Equal(again, body) {
		t.Fatal("manifest encoding is not deterministic")
	}
	if p.ManifestSHA256() != seedDigest(body) || !strings.Contains(p.Manifest().NativeThrough, ":0:") {
		t.Fatal("exact manifest hash or genuine H0 lost")
	}
	for _, chunk := range chunks {
		if bytes.Contains(chunk, []byte("manifest_sha256")) {
			t.Fatal("chunk introduced a circular manifest hash dependency")
		}
		got, err := p.ValidateChunk(chunk)
		if err != nil {
			t.Fatal(err)
		}
		want, err := DecodeSlackSeedChunk(chunk)
		if err != nil || !reflect.DeepEqual(got, want) || len(got.Changes) != 2 || got.Changes[0].Key[0].Kind != "null" {
			t.Fatal("nullable key multiplicity/scalars were lost")
		}
	}
	// Caller-owned metadata cannot change future validation.
	copy := p.Manifest()
	copy.Scope.ChannelIDs[0] = "OTHER"
	copy.Tables[0].Chunks[0].SHA256 = strings.Repeat("0", 64)
	if _, err := p.ValidateChunk(chunks[0]); err != nil || p.Manifest().Scope.ChannelIDs[0] != "CA" {
		t.Fatal("caller modified immutable plan")
	}
	if _, err := p.ValidateComplete(seedCompletion(t, p)); err != nil {
		t.Fatal(err)
	}
	for i := range m.Tables {
		m.Tables[i].Rows, m.Tables[i].SHA256, m.Tables[i].Chunks = "0", seedDigest(nil), []SeedChunkInfo{}
	}
	empty, _ := seedPlan(t, m)
	complete, err := empty.ValidateComplete(seedCompletion(t, empty))
	if err != nil || complete.ExpectedCursor != seedBatchID(m.SeedID, "begin") {
		t.Fatal("empty tables require no fake chunk or native advance")
	}
}

func TestSlackSeedManifestValidation(t *testing.T) {
	cases := map[string]func(*SlackSeedManifest){
		"app":                 func(m *SlackSeedManifest) { m.App = "discrawl" },
		"contract":            func(m *SlackSeedManifest) { m.Contract = "seed-v2" },
		"profile":             func(m *SlackSeedManifest) { m.Profile = "other" },
		"schema":              func(m *SlackSeedManifest) { m.NativeSchema = 8 },
		"binding":             func(m *SlackSeedManifest) { m.BindingID = "" },
		"oversized ID":        func(m *SlackSeedManifest) { m.Archive = strings.Repeat("x", 129) },
		"seed ID":             func(m *SlackSeedManifest) { m.SeedID = strings.ToUpper(m.SeedID) },
		"native negative":     func(m *SlackSeedManifest) { m.NativeThrough = strings.Replace(m.NativeThrough, ":0:", ":-1:", 1) },
		"native leading zero": func(m *SlackSeedManifest) { m.NativeThrough = strings.Replace(m.NativeThrough, ":0:", ":00:", 1) },
		"native overflow": func(m *SlackSeedManifest) {
			m.NativeThrough = strings.Replace(m.NativeThrough, ":0:", ":9223372036854775808:", 1)
		},
		"native entry":         func(m *SlackSeedManifest) { m.NativeThrough += "x" },
		"workspace":            func(m *SlackSeedManifest) { m.Scope.WorkspaceID = "" },
		"empty channels":       func(m *SlackSeedManifest) { m.Scope.ChannelIDs = []string{} },
		"duplicate channels":   func(m *SlackSeedManifest) { m.Scope.ChannelIDs = []string{"CA", "CA"} },
		"null channel":         func(m *SlackSeedManifest) { m.Scope.ChannelIDs = []string{""} },
		"channel NUL":          func(m *SlackSeedManifest) { m.Scope.ChannelIDs = []string{"C\x00"} },
		"missing empty table":  func(m *SlackSeedManifest) { m.Tables = m.Tables[:5] },
		"table order":          func(m *SlackSeedManifest) { m.Tables[0], m.Tables[1] = m.Tables[1], m.Tables[0] },
		"null chunks":          func(m *SlackSeedManifest) { m.Tables[1].Chunks = nil },
		"row sum":              func(m *SlackSeedManifest) { m.Tables[0].Rows = "1" },
		"row overflow":         func(m *SlackSeedManifest) { m.Tables[0].Rows = "18446744073709551616" },
		"row noncanonical":     func(m *SlackSeedManifest) { m.Tables[0].Rows = "02" },
		"table hash":           func(m *SlackSeedManifest) { m.Tables[0].SHA256 = strings.Repeat("0", 64) },
		"chunk hash":           func(m *SlackSeedManifest) { m.Tables[0].Chunks[0].SHA256 = "abc" },
		"empty chunk":          func(m *SlackSeedManifest) { m.Tables[0].Chunks[0].Rows = 0 },
		"chunk row limit":      func(m *SlackSeedManifest) { m.Tables[0].Chunks[0].Rows = MaxChanges + 1 },
		"chunk size":           func(m *SlackSeedManifest) { m.Tables[0].Chunks[0].Bytes = MaxChangeBytes + 1 },
		"zero size":            func(m *SlackSeedManifest) { m.Tables[0].Chunks[0].Bytes = 0 },
		"chunk count":          func(m *SlackSeedManifest) { m.Tables[0].Chunks = make([]SeedChunkInfo, MaxSeedChunks+1) },
		"users without opt-in": func(m *SlackSeedManifest) { m.Tables[2] = m.Tables[0]; m.Tables[2].Name = "users" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := seedFixture(t)
			mutate(&m)
			if _, err := EncodeSlackSeedManifest(m); err == nil {
				t.Fatal("invalid manifest encoded")
			}
			body, _ := json.Marshal(m)
			if p, err := DecodeSlackSeedManifest(body); err == nil || p != nil {
				t.Fatal("invalid manifest decoded")
			}
		})
	}
	// Max chunks is a protocol bound; rows cannot overflow the sum even when
	// callers supply a huge canonical row count in the manifest.
	m, _ := seedFixture(t)
	for i := range m.Tables {
		m.Tables[i].Rows, m.Tables[i].SHA256, m.Tables[i].Chunks = "0", seedDigest(nil), []SeedChunkInfo{}
	}
	m.Tables[0].Chunks = make([]SeedChunkInfo, MaxSeedChunks)
	for i := range m.Tables[0].Chunks {
		m.Tables[0].Chunks[i] = SeedChunkInfo{Rows: MaxChanges, Bytes: MaxChangeBytes, SHA256: strings.Repeat("1", 64)}
	}
	m.Tables[0].Rows = strconv.Itoa(MaxChanges * MaxSeedChunks)
	m.Tables[0].SHA256 = seedTableHash(m.Tables[0].Chunks)
	seedPlan(t, m)
}

func TestSlackSeedDescriptorAndCompletionMismatch(t *testing.T) {
	for _, mismatch := range []string{"table", "count", "size", "hash"} {
		t.Run(mismatch, func(t *testing.T) {
			m, chunks := seedFixture(t)
			switch mismatch {
			case "table":
				m.Tables[1] = m.Tables[0]
				m.Tables[1].Name = "channels"
				m.Tables[0] = SeedTable{Name: "workspaces", Rows: "0", SHA256: seedDigest(nil), Chunks: []SeedChunkInfo{}}
			case "count":
				m.Tables[0].Chunks[0].Rows = 1
				m.Tables[0].Rows = "1"
			case "size":
				m.Tables[0].Chunks[0].Bytes++
			case "hash":
				m.Tables[0].Chunks[0].SHA256 = strings.Repeat("0", 64)
				m.Tables[0].SHA256 = seedTableHash(m.Tables[0].Chunks)
			}
			p, _ := seedPlan(t, m)
			if _, err := p.ValidateChunk(chunks[0]); err == nil {
				t.Fatal("descriptor mismatch accepted")
			}
		})
	}
	m, chunks := seedFixture(t)
	p, _ := seedPlan(t, m)
	for _, field := range []string{"archive", "binding_id", "seed_id", "manifest_sha256", "expected_cursor"} {
		body := seedCompletion(t, p)
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		fields[field] = strings.Repeat("0", 64)
		body, _ = json.Marshal(fields)
		if _, err := p.ValidateComplete(body); err == nil {
			t.Fatalf("completion %s mismatch accepted", field)
		}
	}
	var c SlackSeedChunk
	_ = json.Unmarshal(chunks[0], &c)
	c.ExpectedCursor = ""
	if _, err := EncodeSlackSeedChunk(c); err == nil {
		t.Fatal("unanchored chunk accepted")
	}
	c.ExpectedCursor = seedPreviousCursor(c.SeedID, 0)
	c.Changes[0].Operation, c.Changes[0].Row = "remove", nil
	if _, err := EncodeSlackSeedChunk(c); err == nil {
		t.Fatal("seed removal accepted")
	}
}

func seedSend(ctx context.Context, c *Client, archive string, p *SlackSeedPlan, kind string, body []byte) (SeedReceipt, error) {
	switch kind {
	case "begin":
		return c.BeginSlackSeed(ctx, archive, p, body)
	case "complete":
		return c.CompleteSlackSeed(ctx, archive, p, body)
	default:
		return c.ApplySlackSeedChunk(ctx, archive, p, body)
	}
}

func TestSlackSeedClientExactBytesReplayAndReceipts(t *testing.T) {
	m, chunks := seedFixture(t)
	// Noncanonical accepted bytes must be sent and hashed unchanged, including
	// unsorted scope channels. Sender sorting is only an encoding convenience.
	chunks[0] = append([]byte("\n"), chunks[0]...)
	m.Tables[0].Chunks[0].Bytes, m.Tables[0].Chunks[0].SHA256 = len(chunks[0]), seedDigest(chunks[0])
	m.Tables[0].SHA256 = seedTableHash(m.Tables[0].Chunks)
	manifest, _ := json.MarshalIndent(m, "", "  ")
	p, err := DecodeSlackSeedManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	complete := seedCompletion(t, p)
	bodies := [][]byte{manifest, chunks[0], chunks[1], complete}
	kinds := []string{"begin", "0", "1", "complete"}
	calls := 0
	client := changeClient(t, func(w http.ResponseWriter, r *http.Request) {
		index := calls / 2
		replay := calls%2 == 1
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(body, bodies[index]) || r.ContentLength != int64(len(body)) {
			t.Error("persisted bytes changed")
		}
		wantPath := "/v1/apps/slacrawl/archives/fixture%2Farchive/seeds"
		if index > 0 {
			wantPath += "/" + m.SeedID + "/"
			if index < 3 {
				wantPath += "chunks/"
			}
			wantPath += kinds[index]
		}
		if r.Method != http.MethodPost || r.URL.EscapedPath() != wantPath || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("wrong seed authenticated route")
		}
		id := seedBatchID(m.SeedID, kinds[index])
		_ = json.NewEncoder(w).Encode(SeedReceipt{App: m.App, Archive: m.Archive, BindingID: m.BindingID, BatchID: id, Cursor: id,
			PayloadSHA256: seedDigest(body), Sequence: strconv.Itoa(index + 1), Replayed: replay})
	})
	for i, kind := range kinds {
		for replay := 0; replay < 2; replay++ {
			r, err := seedSend(context.Background(), client, m.Archive, p, kind, bodies[i])
			if err != nil || r.Replayed != (replay == 1) || r.Sequence != strconv.Itoa(i+1) {
				t.Fatalf("receipt/replay: %+v %v", r, err)
			}
		}
	}
	if calls != 8 {
		t.Fatal("hidden retry")
	}
	for _, route := range BaseContract().Routes {
		if strings.Contains(route.Path, "/seeds") {
			t.Fatal("seed receiver advertised")
		}
	}
}

func TestSlackSeedClientsRejectBeforeCredentials(t *testing.T) {
	m, chunks := seedFixture(t)
	p, manifest := seedPlan(t, m)
	client, err := NewClient(Options{Endpoint: "https://remote.test", TokenProvider: tokenProviderFunc(func(context.Context) (string, error) {
		t.Fatal("invalid request reached credential provider")
		return "", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"begin", "0", "complete"} {
		body := manifest
		if kind == "0" {
			body = chunks[0]
		}
		if kind == "complete" {
			body = seedCompletion(t, p)
		}
		if _, err := seedSend(context.Background(), client, "wrong-archive", p, kind, body); err == nil {
			t.Fatal("route mismatch accepted")
		}
		if _, err := seedSend(context.Background(), client, m.Archive, nil, kind, body); err == nil {
			t.Fatal("missing plan accepted")
		}
		if _, err := seedSend(context.Background(), client, m.Archive, p, kind, []byte(`{}`)); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if _, err := client.BeginSlackSeed(context.Background(), m.Archive, p, append(manifest, ' ')); err == nil {
		t.Fatal("changed manifest bytes accepted")
	}
}

func TestSlackSeedClientReceiptMismatchAndConflict(t *testing.T) {
	m, chunks := seedFixture(t)
	p, manifest := seedPlan(t, m)
	for _, kind := range []string{"begin", "0", "complete"} {
		body := manifest
		if kind == "0" {
			body = chunks[0]
		}
		if kind == "complete" {
			body = seedCompletion(t, p)
		}
		for _, field := range []string{"app", "archive", "binding_id", "batch_id", "payload_sha256", "cursor", "sequence"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				client := changeClient(t, func(w http.ResponseWriter, r *http.Request) {
					id := seedBatchID(m.SeedID, kind)
					response := map[string]any{"app": m.App, "archive": m.Archive, "binding_id": m.BindingID, "batch_id": id, "payload_sha256": seedDigest(body), "cursor": id, "sequence": "1", "replayed": false}
					response[field] = "0"
					_ = json.NewEncoder(w).Encode(response)
				})
				if got, err := seedSend(context.Background(), client, m.Archive, p, kind, body); err == nil || got != (SeedReceipt{}) {
					t.Fatal("mismatched receipt accepted")
				}
			})
		}
		calls := 0
		client := changeClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"code":"cursor_conflict"}`)
		})
		_, err := seedSend(context.Background(), client, m.Archive, p, kind, body)
		var remoteErr *Error
		if !errors.As(err, &remoteErr) || remoteErr.Status != 409 || calls != 1 {
			t.Fatal("conflict hidden or retried")
		}
	}
}

func TestSlackSeedStrictJSON(t *testing.T) {
	m, chunks := seedFixture(t)
	p, manifest := seedPlan(t, m)
	complete := seedCompletion(t, p)
	for kind, body := range map[string][]byte{"manifest": manifest, "chunk": chunks[0], "complete": complete} {
		valid := string(body)
		cases := map[string]string{
			"unknown":           strings.Replace(valid, `"contract":`, `"private_path":"secret","contract":`, 1),
			"duplicate":         strings.Replace(valid, `"contract":`, `"contract":"slacrawl-seed-v1","contract":`, 1),
			"case alias":        strings.Replace(valid, `"contract":`, `"Contract":`, 1),
			"escaped duplicate": strings.Replace(valid, `"contract":`, `"contr\u0061ct":"slacrawl-seed-v1","contract":`, 1),
			"missing":           strings.Replace(valid, `"contract":"slacrawl-seed-v1",`, "", 1),
			"null":              strings.Replace(valid, `"contract":"slacrawl-seed-v1"`, `"contract":null`, 1),
			"UTF8":              strings.Replace(valid, "fixture-binding", string([]byte{0xff}), 1),
			"surrogate":         strings.Replace(valid, "fixture-binding", `\ud800`, 1),
			"short escape":      `{"contract":"\u0"}`,
			"trailing slash":    `{"contract":"\`,
			"trailing object":   valid + `{}`,
			"oversize":          strings.Repeat(" ", MaxChangeBytes) + valid,
		}
		var nested []string
		switch kind {
		case "manifest":
			nested = []string{`"workspace_id":"TTEST"`, `"name":"workspaces"`, `"bytes":` + strconv.Itoa(len(chunks[0]))}
		case "chunk":
			nested = []string{`"operation":"upsert"`, `"kind":"null"`}
		}
		for _, field := range nested {
			cases[field+" duplicate"] = strings.Replace(valid, field, field+","+field, 1)
			cases[field+" unknown"] = strings.Replace(valid, field, field+`,"extra":false`, 1)
		}
		for name, body := range cases {
			t.Run(kind+"/"+name, func(t *testing.T) {
				var err error
				switch kind {
				case "manifest":
					_, err = DecodeSlackSeedManifest([]byte(body))
				case "chunk":
					_, err = DecodeSlackSeedChunk([]byte(body))
				case "complete":
					_, err = DecodeSlackSeedComplete([]byte(body))
				}
				if err == nil {
					t.Fatal("invalid JSON accepted")
				}
			})
		}
	}
}

func TestSlackSeedTableHashUsesOrderedBinaryDigests(t *testing.T) {
	m, _ := seedFixture(t)
	m.Tables[0].Chunks = []SeedChunkInfo{{Rows: 1, Bytes: 1, SHA256: strings.Repeat("00", 32)}, {Rows: 1, Bytes: 1, SHA256: strings.Repeat("ff", 32)}}
	binary := append(make([]byte, 32), bytes.Repeat([]byte{255}, 32)...)
	m.Tables[0].SHA256 = seedDigest(binary)
	seedPlan(t, m)
	m.Tables[0].Chunks[0], m.Tables[0].Chunks[1] = m.Tables[0].Chunks[1], m.Tables[0].Chunks[0]
	if _, err := EncodeSlackSeedManifest(m); err == nil {
		t.Fatal("chunk order ignored in table hash")
	}
	m.Tables[0].SHA256 = seedDigest([]byte(m.Tables[0].Chunks[0].SHA256 + m.Tables[0].Chunks[1].SHA256))
	if _, err := EncodeSlackSeedManifest(m); err == nil {
		t.Fatal("hexadecimal text accepted as binary digest concatenation")
	}
}

func TestSlackSeedChunkEncodedByteLimit(t *testing.T) {
	_, bodies := seedFixture(t)
	c, err := DecodeSlackSeedChunk(bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Changes = c.Changes[:1]
	c.Changes[0].Row = []ChangeValue{changeText("")}
	base, err := EncodeSlackSeedChunk(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Changes[0].Row[0] = changeText(strings.Repeat("x", MaxChangeBytes-len(base)))
	body, err := EncodeSlackSeedChunk(c)
	if err != nil || len(body) != MaxChangeBytes {
		t.Fatalf("exact limit: bytes=%d err=%v", len(body), err)
	}
	if _, err := DecodeSlackSeedChunk(append(body, ' ')); err == nil {
		t.Fatal("oversized chunk decoded")
	}
	c.Changes[0].Row[0] = changeText(strings.Repeat("\x00", MaxChangeBytes/5))
	if _, err := EncodeSlackSeedChunk(c); err == nil {
		t.Fatal("JSON escaping exceeded body limit")
	}
}
