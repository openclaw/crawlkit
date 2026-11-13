package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func githubSeedFixture(t *testing.T) (GitHubSeedManifest, [][]byte) {
	t.Helper()
	m := GitHubSeedManifest{Contract: GitHubSeedContract, App: "gitcrawl", Archive: "fixture/archive", BindingID: "fixture-binding",
		SeedID: "0123456789abcdef0123456789abcdef", Profile: "gitcrawl-private-report-v1", NativeSchema: 16,
		Repository: "fixture/repository", RepositoryID: "9223372036854775807",
		ScopeSHA256: strings.Repeat("a", 64), NativeSHA256: strings.Repeat("b", 64)}
	m.NativeThrough = "gitcrawl-native-v1:0123456789abcdef0123456789abcdef:1:83:0:0:" + m.ScopeSHA256 + ":" + m.NativeSHA256

	var bodies [][]byte
	// Rows exercise wire fidelity; native column/key interpretation stays in
	// Gitcrawl and its receiver, so this fixture does not copy their schema.
	for _, name := range githubSeedTables {
		table := SeedTable{Name: name, Rows: "0", SHA256: seedDigest(nil), Chunks: []SeedChunkInfo{}}
		if name == "repositories" || name == "comments" {
			c := GitHubSeedChunk{Contract: m.Contract, App: m.App, Archive: m.Archive, BindingID: m.BindingID, SeedID: m.SeedID,
				Index: len(bodies), ExpectedCursor: seedPreviousCursor(m.SeedID, len(bodies)),
				Changes: []Change{{Table: name, Operation: "upsert", Key: []ChangeValue{{Kind: "null"}},
					Row: []ChangeValue{{Kind: "null"}, changeText(""), changeText("☃\x00tail"), changeInteger("9223372036854775807"), changeInteger("-9223372036854775808"),
						changeText(`{"raw":"private\u0000evidence"}`), changeText("2026-11-14T01:02:03.123456789Z")}}}}
			// Raw seed rows retain multiplicity, even for an ambiguous NULL key.
			c.Changes = append(c.Changes, c.Changes[0])
			body, err := EncodeGitHubSeedChunk(c)
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

func githubSeedPlan(t *testing.T, m GitHubSeedManifest) (*GitHubSeedPlan, []byte) {
	t.Helper()
	body, err := EncodeGitHubSeedManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeGitHubSeedManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	return p, body
}

func githubSeedCompletion(t *testing.T, p *GitHubSeedPlan) []byte {
	t.Helper()
	m := p.Manifest()
	body, err := EncodeGitHubSeedComplete(GitHubSeedComplete{Contract: m.Contract, App: m.App, Archive: m.Archive, BindingID: m.BindingID,
		SeedID: m.SeedID, ManifestSHA256: p.ManifestSHA256(), ExpectedCursor: seedPreviousCursor(m.SeedID, len(p.chunks))})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestGitHubSeedPlanH0HashOrderAndRowFidelity(t *testing.T) {
	m, chunks := githubSeedFixture(t)
	p, body := githubSeedPlan(t, m)
	if again, err := EncodeGitHubSeedManifest(m); err != nil || !bytes.Equal(again, body) {
		t.Fatal("manifest encoding is not deterministic")
	}
	if p.ManifestSHA256() != seedDigest(body) || !strings.Contains(p.Manifest().NativeThrough, ":83:0:0:") {
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
		want, err := DecodeGitHubSeedChunk(chunk)
		if err != nil || !reflect.DeepEqual(got, want) || len(got.Changes) != 2 || got.Changes[0].Key[0].Kind != "null" {
			t.Fatal("nullable key multiplicity/scalars were lost")
		}
		values := got.Changes[0].Row
		if values[0].Value != nil || *values[1].Value != "" || *values[2].Value != "☃\x00tail" || *values[3].Value != "9223372036854775807" ||
			*values[4].Value != "-9223372036854775808" || *values[5].Value != `{"raw":"private\u0000evidence"}` || *values[6].Value != "2026-11-14T01:02:03.123456789Z" {
			t.Fatal("native scalar, raw evidence or publication precision changed")
		}
	}
	// Caller-owned metadata cannot change future validation.
	copy := p.Manifest()
	copy.Tables[0].Chunks[0].SHA256 = strings.Repeat("0", 64)
	if _, err := p.ValidateChunk(chunks[0]); err != nil {
		t.Fatal("caller modified immutable plan")
	}
	if _, err := p.ValidateComplete(githubSeedCompletion(t, p)); err != nil {
		t.Fatal(err)
	}
	oldToken := m.NativeThrough
	m.NativeSHA256 = strings.Repeat("c", 64)
	if _, err := EncodeGitHubSeedManifest(m); err == nil {
		t.Fatal("same token accepted different inventory")
	}
	m.NativeThrough = strings.TrimSuffix(oldToken, p.Manifest().NativeSHA256) + m.NativeSHA256
	other, _ := githubSeedPlan(t, m)
	if other.Manifest().NativeThrough == oldToken || other.ManifestSHA256() == p.ManifestSHA256() {
		t.Fatal("same-head inventories collapsed")
	}
	for i := range m.Tables {
		m.Tables[i].Rows, m.Tables[i].SHA256, m.Tables[i].Chunks = "0", seedDigest(nil), []SeedChunkInfo{}
	}
	empty, _ := githubSeedPlan(t, m)
	complete, err := empty.ValidateComplete(githubSeedCompletion(t, empty))
	if err != nil || complete.ExpectedCursor != seedBatchID(m.SeedID, "begin") {
		t.Fatal("empty tables require no fake chunk or native advance")
	}
}

func TestGitHubSeedManifestValidation(t *testing.T) {
	cases := map[string]func(*GitHubSeedManifest){
		"contract":                   func(m *GitHubSeedManifest) { m.Contract = SlackSeedContract },
		"app":                        func(m *GitHubSeedManifest) { m.App = "slacrawl" },
		"profile":                    func(m *GitHubSeedManifest) { m.Profile = "gitcrawl-public-v1" },
		"schema":                     func(m *GitHubSeedManifest) { m.NativeSchema = 15 },
		"binding":                    func(m *GitHubSeedManifest) { m.BindingID = "" },
		"seed":                       func(m *GitHubSeedManifest) { m.SeedID = strings.ToUpper(m.SeedID) },
		"repository case":            func(m *GitHubSeedManifest) { m.Repository = "Fixture/repository" },
		"repository missing":         func(m *GitHubSeedManifest) { m.Repository = "fixture/" },
		"repository extra":           func(m *GitHubSeedManifest) { m.Repository = "fixture/repository/extra" },
		"repository space":           func(m *GitHubSeedManifest) { m.Repository = "fixture/repo name" },
		"repository NUL":             func(m *GitHubSeedManifest) { m.Repository = "fixture/repo\x00" },
		"repository size":            func(m *GitHubSeedManifest) { m.Repository = "fixture/" + strings.Repeat("a", 128) },
		"repository ID zero":         func(m *GitHubSeedManifest) { m.RepositoryID = "0" },
		"repository ID overflow":     func(m *GitHubSeedManifest) { m.RepositoryID = "9223372036854775808" },
		"repository ID noncanonical": func(m *GitHubSeedManifest) { m.RepositoryID = "01" },
		"scope hash":                 func(m *GitHubSeedManifest) { m.ScopeSHA256 = strings.Repeat("A", 64) },
		"inventory hash":             func(m *GitHubSeedManifest) { m.NativeSHA256 = "abcd" },
		"scope mismatch":             func(m *GitHubSeedManifest) { m.ScopeSHA256 = strings.Repeat("c", 64) },
		"inventory mismatch":         func(m *GitHubSeedManifest) { m.NativeSHA256 = strings.Repeat("d", 64) },
		"missing table":              func(m *GitHubSeedManifest) { m.Tables = m.Tables[:6] },
		"table order":                func(m *GitHubSeedManifest) { m.Tables[0], m.Tables[1] = m.Tables[1], m.Tables[0] },
		"null chunks":                func(m *GitHubSeedManifest) { m.Tables[0].Chunks = nil },
		"row sum":                    func(m *GitHubSeedManifest) { m.Tables[0].Rows = "3" },
		"table hash":                 func(m *GitHubSeedManifest) { m.Tables[0].SHA256 = strings.Repeat("0", 64) },
	}
	for i, invalid := range map[int][]string{0: {"gitcrawl-native-v2"}, 1: {"", strings.Repeat("A", 32)}, 2: {"0", "-1", "+1", "01", "9223372036854775808"}, 3: {"0", "-1", "01"}, 4: {"-1", "00"}, 5: {"-1", "1", "00"}} {
		for _, value := range invalid {
			cases["anchor/"+strconv.Itoa(i)+"/"+value] = func(m *GitHubSeedManifest) {
				parts := strings.Split(m.NativeThrough, ":")
				parts[i] = value
				m.NativeThrough = strings.Join(parts, ":")
			}
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := githubSeedFixture(t)
			mutate(&m)
			if _, err := EncodeGitHubSeedManifest(m); err == nil {
				t.Fatal("invalid manifest encoded")
			}
			body, _ := json.Marshal(m)
			if p, err := DecodeGitHubSeedManifest(body); err == nil || p != nil {
				t.Fatal("invalid manifest decoded")
			}
		})
	}
}

func TestGitHubSeedDescriptorAndCompletionMismatch(t *testing.T) {
	for _, mismatch := range []string{"table", "count", "size", "hash"} {
		t.Run(mismatch, func(t *testing.T) {
			m, chunks := githubSeedFixture(t)
			switch mismatch {
			case "table":
				m.Tables[1] = m.Tables[0]
				m.Tables[1].Name = "threads"
				m.Tables[0] = SeedTable{Name: "repositories", Rows: "0", SHA256: seedDigest(nil), Chunks: []SeedChunkInfo{}}
			case "count":
				m.Tables[0].Chunks[0].Rows = 1
				m.Tables[0].Rows = "1"
			case "size":
				m.Tables[0].Chunks[0].Bytes++
			case "hash":
				m.Tables[0].Chunks[0].SHA256 = strings.Repeat("0", 64)
				m.Tables[0].SHA256 = seedTableHash(m.Tables[0].Chunks)
			}
			p, _ := githubSeedPlan(t, m)
			if _, err := p.ValidateChunk(chunks[0]); err == nil {
				t.Fatal("descriptor mismatch accepted")
			}
		})
	}
	m, chunks := githubSeedFixture(t)
	p, _ := githubSeedPlan(t, m)
	for _, field := range []string{"archive", "binding_id", "seed_id", "manifest_sha256", "expected_cursor"} {
		body := githubSeedCompletion(t, p)
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		fields[field] = strings.Repeat("0", 64)
		body, _ = json.Marshal(fields)
		if _, err := p.ValidateComplete(body); err == nil {
			t.Fatalf("completion %s mismatch accepted", field)
		}
	}
	var c GitHubSeedChunk
	_ = json.Unmarshal(chunks[0], &c)
	c.ExpectedCursor = ""
	if _, err := EncodeGitHubSeedChunk(c); err == nil {
		t.Fatal("unanchored chunk accepted")
	}
	c.ExpectedCursor = seedPreviousCursor(c.SeedID, 0)
	c.Changes[0].Operation, c.Changes[0].Row = "remove", nil
	if _, err := EncodeGitHubSeedChunk(c); err == nil {
		t.Fatal("seed removal accepted")
	}
}

func githubSeedSend(ctx context.Context, c *Client, archive string, p *GitHubSeedPlan, kind string, body []byte) (SeedReceipt, error) {
	switch kind {
	case "begin":
		return c.BeginGitHubSeed(ctx, archive, p, body)
	case "complete":
		return c.CompleteGitHubSeed(ctx, archive, p, body)
	default:
		return c.ApplyGitHubSeedChunk(ctx, archive, p, body)
	}
}

func TestGitHubSeedClientExactBytesReplayAndReceipts(t *testing.T) {
	for name, fixture := range map[string]func(*testing.T) (GitHubSeedManifest, [][]byte){
		"private": githubSeedFixture, "cluster": githubClusterSeedFixture,
	} {
		t.Run(name, func(t *testing.T) { testGitHubSeedClientExactBytesReplayAndReceipts(t, fixture) })
	}
}

func testGitHubSeedClientExactBytesReplayAndReceipts(t *testing.T, fixture func(*testing.T) (GitHubSeedManifest, [][]byte)) {
	t.Helper()
	m, chunks := fixture(t)
	// Noncanonical accepted bytes must be sent and hashed unchanged, including
	// whitespace. Sender encoding is only a convenience.
	chunks[0] = append([]byte("\n"), chunks[0]...)
	m.Tables[0].Chunks[0].Bytes, m.Tables[0].Chunks[0].SHA256 = len(chunks[0]), seedDigest(chunks[0])
	m.Tables[0].SHA256 = seedTableHash(m.Tables[0].Chunks)
	manifest, _ := json.MarshalIndent(m, "", "  ")
	p, err := DecodeGitHubSeedManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	complete := githubSeedCompletion(t, p)
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
		wantPath := "/v1/apps/gitcrawl/archives/fixture%2Farchive/seeds"
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
			r, err := githubSeedSend(context.Background(), client, m.Archive, p, kind, bodies[i])
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

func TestGitHubSeedClientsRejectBeforeCredentials(t *testing.T) {
	m, chunks := githubSeedFixture(t)
	p, manifest := githubSeedPlan(t, m)
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
			body = githubSeedCompletion(t, p)
		}
		if _, err := githubSeedSend(context.Background(), client, "wrong-archive", p, kind, body); err == nil {
			t.Fatal("route mismatch accepted")
		}
		if _, err := githubSeedSend(context.Background(), client, m.Archive, nil, kind, body); err == nil {
			t.Fatal("missing plan accepted")
		}
		if _, err := githubSeedSend(context.Background(), client, m.Archive, p, kind, []byte(`{}`)); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if _, err := client.BeginGitHubSeed(context.Background(), m.Archive, p, append(manifest, ' ')); err == nil {
		t.Fatal("changed manifest bytes accepted")
	}
}

func TestGitHubSeedClientReceiptMismatchAndConflict(t *testing.T) {
	m, chunks := githubSeedFixture(t)
	p, manifest := githubSeedPlan(t, m)
	for _, kind := range []string{"begin", "0", "complete"} {
		body := manifest
		if kind == "0" {
			body = chunks[0]
		}
		if kind == "complete" {
			body = githubSeedCompletion(t, p)
		}
		for _, field := range []string{"app", "archive", "binding_id", "batch_id", "payload_sha256", "cursor", "sequence"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				client := changeClient(t, func(w http.ResponseWriter, r *http.Request) {
					id := seedBatchID(m.SeedID, kind)
					response := map[string]any{"app": m.App, "archive": m.Archive, "binding_id": m.BindingID, "batch_id": id, "payload_sha256": seedDigest(body), "cursor": id, "sequence": "1", "replayed": false}
					response[field] = "0"
					_ = json.NewEncoder(w).Encode(response)
				})
				if got, err := githubSeedSend(context.Background(), client, m.Archive, p, kind, body); err == nil || got != (SeedReceipt{}) {
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
		_, err := githubSeedSend(context.Background(), client, m.Archive, p, kind, body)
		var remoteErr *Error
		if !errors.As(err, &remoteErr) || remoteErr.Status != 409 || calls != 1 {
			t.Fatal("conflict hidden or retried")
		}
	}
}

func TestGitHubSeedStrictJSON(t *testing.T) {
	m, chunks := githubSeedFixture(t)
	p, manifest := githubSeedPlan(t, m)
	complete := githubSeedCompletion(t, p)
	for kind, body := range map[string][]byte{"manifest": manifest, "chunk": chunks[0], "complete": complete} {
		valid := string(body)
		cases := map[string]string{
			"unknown":           strings.Replace(valid, `"contract":`, `"private_path":"secret","contract":`, 1),
			"duplicate":         strings.Replace(valid, `"contract":`, `"contract":"gitcrawl-seed-v1","contract":`, 1),
			"case alias":        strings.Replace(valid, `"contract":`, `"Contract":`, 1),
			"escaped duplicate": strings.Replace(valid, `"contract":`, `"contr\u0061ct":"gitcrawl-seed-v1","contract":`, 1),
			"missing":           strings.Replace(valid, `"contract":"gitcrawl-seed-v1",`, "", 1),
			"null":              strings.Replace(valid, `"contract":"gitcrawl-seed-v1"`, `"contract":null`, 1),
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
			nested = []string{`"name":"repositories"`, `"bytes":` + strconv.Itoa(len(chunks[0]))}
		case "chunk":
			nested = []string{`"operation":"upsert"`, `"kind":"null"`}
			cases["explicit null scalar value"] = strings.Replace(valid, `"kind":"null"`, `"kind":"null","value":null`, 1)
			cases["integer overflow"] = strings.Replace(valid, `"9223372036854775807"`, `"9223372036854775808"`, 1)
			cases["integer number coercion"] = strings.Replace(valid, `"9223372036854775807"`, `9223372036854775807`, 1)
			cases["invalid scalar UTF8"] = strings.Replace(valid, "☃", string([]byte{0xff}), 1)
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
					_, err = DecodeGitHubSeedManifest([]byte(body))
				case "chunk":
					_, err = DecodeGitHubSeedChunk([]byte(body))
				case "complete":
					_, err = DecodeGitHubSeedComplete([]byte(body))
				}
				if err == nil {
					t.Fatal("invalid JSON accepted")
				}
			})
		}
	}
}

func TestGitHubSeedChunkEncodedByteLimit(t *testing.T) {
	_, bodies := githubSeedFixture(t)
	c, err := DecodeGitHubSeedChunk(bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Changes = c.Changes[:1]
	c.Changes[0].Row = []ChangeValue{changeText("")}
	base, err := EncodeGitHubSeedChunk(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Changes[0].Row[0] = changeText(strings.Repeat("x", MaxChangeBytes-len(base)))
	body, err := EncodeGitHubSeedChunk(c)
	if err != nil || len(body) != MaxChangeBytes {
		t.Fatalf("exact limit: bytes=%d err=%v", len(body), err)
	}
	if _, err := DecodeGitHubSeedChunk(append(body, ' ')); err == nil {
		t.Fatal("oversized chunk decoded")
	}
	c.Changes[0].Row[0] = changeText(strings.Repeat("\x00", MaxChangeBytes/5))
	if _, err := EncodeGitHubSeedChunk(c); err == nil {
		t.Fatal("JSON escaping exceeded body limit")
	}
}
