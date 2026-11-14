package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreservationContractAcceptsOptionalAdminRoutes(t *testing.T) {
	contract := BaseContract()
	contract.Routes = append(contract.Routes, RouteSpec{Method: http.MethodGet, Path: "/v1/apps/:app/archives/:archive/preservation-manifest", Auth: AuthAdmin})
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPreservationRoundTrip(t *testing.T) {
	ctx := context.Background()
	input := []byte{0x28, 0xb5, 0x2f, 0xfd, 0, 255, 13, 10, 1}
	source := filepath.Join(t.TempDir(), "closed.sqlite.zst")
	if err := os.WriteFile(source, input, 0600); err != nil {
		t.Fatal(err)
	}
	sources := []PreservationSource{{Name: "native.sqlite.zst", Path: source}}
	manifest, err := PlanPreservation(ctx, "discrawl", "discrawl/example", sources, 4)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string][]byte{}
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Error("missing auth")
			w.WriteHeader(403)
			return
		}
		if !strings.HasPrefix(r.URL.EscapedPath(), "/prefix/v1/apps/discrawl/archives/discrawl%2Fexample/") {
			t.Errorf("bad scoped route: %s", r.URL.EscapedPath())
		}
		digest := r.URL.Query().Get("sha256")
		key := r.URL.Path + "/" + digest
		if r.Method == http.MethodPut {
			puts++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.ContentLength != int64(len(body)) || preservationDigest(body) != digest {
				t.Error("upload length/digest mismatch")
			}
			if prior, ok := objects[key]; ok && !bytes.Equal(prior, body) {
				t.Error("immutable object changed")
			}
			objects[key] = body
			if strings.HasSuffix(r.URL.Path, "preservation-manifest") {
				_ = json.NewEncoder(w).Encode(map[string]any{"sha256": digest, "verification": "chunk-checksums", "restore_qualified": false})
			} else {
				_ = json.NewEncoder(w).Encode(PreservationChunk{SHA256: digest, Size: int64(len(body))})
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "preservation-chunk") {
			var m PreservationManifest
			mkey := strings.TrimSuffix(r.URL.Path, "preservation-chunk") + "preservation-manifest/" + digest
			if err := json.Unmarshal(objects[mkey], &m); err != nil {
				t.Error(err)
				w.WriteHeader(404)
				return
			}
			var index int
			if r.URL.Query().Get("file") != "0" {
				t.Error("missing file selector")
			}
			switch r.URL.Query().Get("chunk") {
			case "0":
				index = 0
			case "1":
				index = 1
			case "2":
				index = 2
			default:
				t.Error("bad chunk selector")
			}
			key = r.URL.Path + "/" + m.Files[0].Chunks[index].SHA256
		}
		body, ok := objects[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	client, err := NewClient(Options{Endpoint: server.URL + "/prefix", TokenProvider: StaticToken("private-test-token")})
	if err != nil {
		t.Fatal(err)
	}
	var receipt PreservationReceipt
	for i := 0; i < 2; i++ {
		receipt, err = client.UploadPreservation(ctx, manifest, sources)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.RestoreQualified {
			t.Fatal("upload claimed a qualified restore")
		}
	}
	if puts != 8 || len(objects) != 4 {
		t.Fatalf("uploads=%d objects=%d", puts, len(objects))
	}
	var restored bytes.Buffer
	if err := client.DownloadPreservationFile(ctx, manifest.App, manifest.Archive, receipt.ManifestSHA256, 0, &restored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.Bytes(), input) {
		t.Fatal("opaque bytes changed")
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(after, input) {
		t.Fatal("source changed")
	}
}

func TestPreservationRejectsChangedSourceBeforeManifest(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("123456"), 0600); err != nil {
		t.Fatal(err)
	}
	sources := []PreservationSource{{Name: "backup", Path: source}}
	m, err := PlanPreservation(context.Background(), "gitcrawl", "example", sources, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("654321"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	c, _ := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("test")})
	if _, err := c.UploadPreservation(context.Background(), m, sources); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("got %v", err)
	}
	if requests != 0 {
		t.Fatal("sent changed source")
	}
}

func TestPreservationRejectsCorruptDownload(t *testing.T) {
	for _, corrupt := range []string{"chunk", "whole", "oversize", "manifest", "scope", "path"} {
		t.Run(corrupt, func(t *testing.T) {
			body := []byte("abc")
			m := PreservationManifest{Version: 1, App: "gitcrawl", Archive: "example", Files: []PreservationFile{{Path: "source", Size: 3, SHA256: preservationDigest(body), Chunks: []PreservationChunk{{Size: 3, SHA256: preservationDigest(body)}}}}}
			switch corrupt {
			case "whole":
				m.Files[0].SHA256 = strings.Repeat("0", 64)
			case "scope":
				m.Archive = "other"
			case "path":
				m.Files[0].Path = "../escape"
			}
			manifest, _ := json.Marshal(m)
			digest := preservationDigest(manifest)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "preservation-manifest") {
					if corrupt == "manifest" {
						manifest = append(manifest, ' ')
					}
					_, _ = w.Write(manifest)
					return
				}
				switch corrupt {
				case "chunk":
					body = []byte("xyz")
				case "oversize":
					body = []byte("abcdef")
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()
			c, _ := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("test")})
			var out bytes.Buffer
			if err := c.DownloadPreservationFile(context.Background(), "gitcrawl", "example", digest, 0, &out); err == nil {
				t.Fatal("accepted corrupt download")
			}
			if out.Len() > 3 {
				t.Fatal("wrote beyond declared size")
			}
		})
	}
}

func TestPreservationPlanningRejectsAmbiguousPathsAndBounds(t *testing.T) {
	file := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(file, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{{"../escape"}, {"con"}, {"trailing."}, {"a", "A"}, {"a", "a/b"}, {"a/b", "a"}} {
		var sources []PreservationSource
		for _, name := range names {
			sources = append(sources, PreservationSource{Name: name, Path: file})
		}
		if _, err := PlanPreservation(context.Background(), "gitcrawl", "example", sources, 2); err == nil {
			t.Fatalf("accepted %v", names)
		}
	}
	for _, size := range []int64{0, PreservationChunkBytes + 1} {
		if _, err := PlanPreservation(context.Background(), "gitcrawl", "example", []PreservationSource{{Name: "a", Path: file}}, size); err == nil {
			t.Fatal("accepted invalid chunk size")
		}
	}
	if err := os.WriteFile(file, bytes.Repeat([]byte("x"), 257), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanPreservation(context.Background(), "gitcrawl", "example", []PreservationSource{{Name: "a", Path: file}}, 1); err == nil {
		t.Fatal("accepted overlarge plan")
	}
}

func TestPreservationPlansBoundedSectionsOfLargerFiles(t *testing.T) {
	data := bytes.Repeat([]byte("abcdef"), 100)
	file := filepath.Join(t.TempDir(), "large.pack")
	if err := os.WriteFile(file, data, 0o400); err != nil {
		t.Fatal(err)
	}
	for _, span := range [][2]int64{{0, 3}, {257, 3}, {597, 3}} {
		source := PreservationSource{Name: "part", Path: file, Offset: span[0], Length: span[1]}
		manifest, err := PlanPreservation(context.Background(), "gitcrawl", "example", []PreservationSource{source}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := manifest.Files[0]; got.Size != 3 || got.SHA256 != preservationDigest(data[span[0]:span[0]+span[1]]) {
			t.Fatalf("wrong section: %+v", got)
		}
	}
	for _, span := range [][2]int64{{-1, 1}, {0, -1}, {1, 0}, {599, 2}, {601, 1}, {1, 1<<63 - 1}} {
		source := PreservationSource{Name: "part", Path: file, Offset: span[0], Length: span[1]}
		if _, err := PlanPreservation(context.Background(), "gitcrawl", "example", []PreservationSource{source}, 1); err == nil {
			t.Fatalf("accepted out-of-bounds section %v", span)
		}
	}
}

func TestPreservationRejectsFalseReceipt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(file, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	sources := []PreservationSource{{Name: "source", Path: file}}
	m, err := PlanPreservation(context.Background(), "gitcrawl", "example", sources, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"chunk", "manifest", "qualification"} {
		t.Run(bad, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				digest := r.URL.Query().Get("sha256")
				if strings.HasSuffix(r.URL.Path, "preservation-chunk") {
					if bad == "chunk" {
						digest = strings.Repeat("0", 64)
					}
					_ = json.NewEncoder(w).Encode(PreservationChunk{Size: 3, SHA256: digest})
					return
				}
				if bad == "manifest" {
					digest = strings.Repeat("0", 64)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"sha256": digest, "verification": "chunk-checksums", "restore_qualified": bad == "qualification"})
			}))
			defer server.Close()
			c, _ := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("test")})
			if _, err := c.UploadPreservation(context.Background(), m, sources); err == nil {
				t.Fatal("accepted false receipt")
			}
		})
	}
}
