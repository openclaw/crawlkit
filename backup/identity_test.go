package backup

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureIdentityConcurrentCreatorsAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "identity.age")
	const count = 16
	start := make(chan struct{})
	results := make([]string, count)
	errs := make([]error, count)
	var workers sync.WaitGroup
	for i := range count {
		workers.Go(func() {
			<-start
			results[i], errs[i] = EnsureIdentity(path)
		})
	}
	close(start)
	workers.Wait()
	want, err := RecipientFromIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range count {
		if errs[i] != nil || results[i] != want {
			t.Errorf("creator %d: recipient=%q error=%v, want surviving %q", i, results[i], errs[i], want)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("identity staging files leaked: %v, %v", entries, err)
	}
}

func TestEnsureIdentityPreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.age")
	first, err := EnsureIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureIdentity(path)
	if err != nil || second != first {
		t.Fatalf("existing identity = %q, %v", second, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("existing identity changed")
	}
	if err := os.WriteFile(path, []byte("invalid existing identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureIdentity(path); err == nil {
		t.Fatal("invalid identity accepted")
	}
	after, err = os.ReadFile(path)
	if err != nil || string(after) != "invalid existing identity" {
		t.Fatalf("invalid identity was replaced: %q, %v", after, err)
	}
}
