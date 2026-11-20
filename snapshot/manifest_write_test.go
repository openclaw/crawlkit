package snapshot

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestWriteManifestPreservesOpenReader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not guarantee atomic rename over an open file")
	}
	root := t.TempDir()
	previous := Manifest{Version: 1, Files: map[string]string{"generation": "previous"}}
	if err := WriteManifest(root, previous); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(filepath.Join(root, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	current := Manifest{Version: 1, Files: map[string]string{"generation": "current"}}
	if err := WriteManifest(root, current); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var observed Manifest
	if err := json.Unmarshal(data, &observed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(observed, previous) {
		t.Fatalf("open reader saw rewritten manifest: %+v", observed)
	}
	got, err := ReadManifest(root)
	if err != nil || !reflect.DeepEqual(got, current) {
		t.Fatalf("new reader: got %+v, err %v", got, err)
	}
}

func TestWriteManifestFailedPublicationCleansTemporaryFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ManifestName)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "keep")
	if err := os.WriteFile(marker, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(root, Manifest{Version: 1}); err == nil {
		t.Fatal("publication over a directory succeeded")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "previous" {
		t.Fatalf("existing target changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != ManifestName {
		t.Fatalf("publication left temporary files: %v, %v", entries, err)
	}
}
