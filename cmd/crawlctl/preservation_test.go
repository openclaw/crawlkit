package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openclaw/crawlkit/remote"
)

func TestPreservationFileRangesReassembleOriginal(t *testing.T) {
	data := []byte("first volume\x00second volume\xff")
	source := filepath.Join(t.TempDir(), "source.pack")
	if err := os.WriteFile(source, data, 0o400); err != nil {
		t.Fatal(err)
	}
	digest := func(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
	objects := map[string][]byte{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		sha := r.URL.Query().Get("sha256")
		if r.Header.Get("Authorization") != "Bearer range-test" {
			t.Error("missing credential")
		}
		key := r.URL.Path + "/" + sha
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			if digest(body) != sha {
				t.Error("wrong digest")
			}
			objects[key] = body
			if strings.HasSuffix(r.URL.Path, "/preservation-manifest") {
				_ = json.NewEncoder(w).Encode(map[string]any{"sha256": sha, "verification": "chunk-checksums", "restore_qualified": false})
			} else {
				_ = json.NewEncoder(w).Encode(remote.PreservationChunk{SHA256: sha, Size: int64(len(body))})
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/preservation-chunk") {
			var manifest remote.PreservationManifest
			_ = json.Unmarshal(objects[strings.TrimSuffix(r.URL.Path, "preservation-chunk")+"preservation-manifest/"+sha], &manifest)
			index, _ := strconv.Atoi(r.URL.Query().Get("chunk"))
			key = r.URL.Path + "/" + manifest.Files[0].Chunks[index].SHA256
		}
		_, _ = w.Write(objects[key])
	}))
	defer server.Close()
	t.Setenv("CRAWL_REMOTE_TOKEN", "range-test")
	var restored []byte
	for i, span := range [][2]int{{0, 13}, {13, len(data) - 13}} {
		archive := "pack/" + strconv.Itoa(i)
		common := []string{"--endpoint", server.URL, "--app", "gitcrawl", "--archive", archive}
		var output bytes.Buffer
		a := app{stdout: &output, stderr: io.Discard}
		args := append([]string{"upload"}, common...)
		args = append(args, "--file-range", "part="+strconv.Itoa(span[0])+":"+strconv.Itoa(span[1])+":"+source, "--chunk-size", "4")
		if err := a.runPreservation(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		var receipt remote.PreservationReceipt
		if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Manifest.Files[0].SHA256 != digest(data[span[0]:span[0]+span[1]]) || receipt.RestoreQualified {
			t.Fatal("range receipt does not describe the exact section")
		}
		destination := filepath.Join(t.TempDir(), "restored")
		args = append([]string{"download"}, common...)
		args = append(args, "--manifest", receipt.ManifestSHA256, "--output", destination)
		if err := a.runPreservation(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		part, err := os.ReadFile(filepath.Join(destination, "part"))
		if err != nil {
			t.Fatal(err)
		}
		restored = append(restored, part...)
	}
	if !bytes.Equal(restored, data) {
		t.Fatal("reassembled original changed")
	}
	if original, err := os.ReadFile(source); err != nil || !bytes.Equal(original, data) {
		t.Fatal("source changed")
	}
}

func TestPreservationArguments(t *testing.T) {
	common := []string{"--endpoint", "http://127.0.0.1:1", "--app", "discrawl", "--archive", "example"}
	tests := [][]string{
		nil, {"unknown"}, {"upload"}, {"upload", "--file", "backup"},
		{"upload", "--file", "=source"}, {"upload", "--file", "backup="},
		{"upload", "--file", "backup=source", "--chunk-size", "0"},
		{"upload", "--file", "backup=source", "--chunk-size", "67108865"},
		{"upload", "--file", "backup=source", "--timeout", "0s"},
		{"upload", "--file", "backup=source", "--token-env", ""},
		{"upload", "--file", "backup=source", "extra"},
		{"download", "--output", "new-output"},
		{"download", "--manifest", strings.Repeat("a", 64)},
		{"download", "--file", "backup=source"},
		{"upload", "--file-range", "part=0:1"},
		{"upload", "--file-range", "part=0:1:"},
		{"upload", "--file-range", "=0:1:source"},
		{"upload", "--file-range", "part=-1:1:source"},
		{"upload", "--file-range", "part=0:0:source"},
		{"upload", "--file-range", "part=0:9223372036854775808:source"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var full []string
			if len(args) > 0 {
				full = append(full, args[0])
				full = append(full, common...)
				full = append(full, args[1:]...)
			}
			var stdout bytes.Buffer
			a := app{stdout: &stdout, stderr: io.Discard}
			var usage usageError
			if err := a.runPreservation(context.Background(), full); !errors.As(err, &usage) {
				t.Fatalf("error = %v, want usage error", err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("unexpected success output: %s", stdout.String())
			}
		})
	}
}

func TestPreservationDownloadRejectsExistingOutputBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("CRAWL_REMOTE_TOKEN", "synthetic-test-token")
	for _, kind := range []string{"directory", "file", "dangling symlink"} {
		t.Run(kind, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "existing")
			preserved := output
			switch kind {
			case "directory":
				if err := os.Mkdir(output, 0o700); err != nil {
					t.Fatal(err)
				}
				preserved = filepath.Join(output, "sentinel")
			case "dangling symlink":
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation requires Windows privileges")
				}
				if err := os.Symlink("missing-target", output); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "dangling symlink" {
				if err := os.WriteFile(preserved, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout bytes.Buffer
			a := app{stdout: &stdout, stderr: io.Discard}
			err := a.runPreservation(context.Background(), []string{
				"download", "--endpoint", server.URL, "--app", "discrawl", "--archive", "example",
				"--manifest", strings.Repeat("a", 64), "--output", output,
			})
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("error = %v", err)
			}
			if requests.Load() != 0 || stdout.Len() != 0 {
				t.Fatalf("requests = %d, output = %q", requests.Load(), stdout.String())
			}
			if kind == "dangling symlink" {
				if target, err := os.Readlink(output); err != nil || target != "missing-target" {
					t.Fatalf("symlink changed: %q, %v", target, err)
				}
			} else if body, err := os.ReadFile(preserved); err != nil || string(body) != "keep" {
				t.Fatalf("existing data changed: %q, %v", body, err)
			}
		})
	}
}

func TestPreservationDownloadRetainsPrivatePartialOutput(t *testing.T) {
	data := []byte("opaque backup")
	digest := func(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
	manifest := remote.PreservationManifest{
		Version: 1, App: "discrawl", Archive: "example",
		Files: []remote.PreservationFile{{
			Path: "native/backup.sqlite", SHA256: digest(data), Size: int64(len(data)),
			Chunks: []remote.PreservationChunk{{SHA256: digest(data), Size: int64(len(data))}},
		}},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRESERVATION_TEST_TOKEN", "synthetic-test-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
			t.Error("custom token environment was not used")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/preservation-manifest"):
			_, _ = w.Write(body)
		case strings.HasSuffix(r.URL.Path, "/preservation-chunk"):
			_, _ = w.Write([]byte("broken"))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "download")
	var stdout bytes.Buffer
	a := app{stdout: &stdout, stderr: io.Discard}
	err = a.runPreservation(context.Background(), []string{
		"download", "--endpoint", server.URL, "--app", "discrawl", "--archive", "example",
		"--manifest", digest(body), "--output", output, "--token-env", "PRESERVATION_TEST_TOKEN",
	})
	if err == nil || !strings.Contains(err.Error(), "partial download retained at "+strconv.Quote(output)+":") || stdout.Len() != 0 {
		t.Fatalf("failed download error = %v, output = %q", err, stdout.String())
	}
	filePath := filepath.Join(output, "native", "backup.sqlite")
	if got, err := os.ReadFile(filePath); err != nil || string(got) != "broken" {
		t.Fatalf("retained data = %q, error = %v", got, err)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{output: 0o700, filepath.Dir(filePath): 0o700, filePath: 0o600} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
			}
		}
	}
}
