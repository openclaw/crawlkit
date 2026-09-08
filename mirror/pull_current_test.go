package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullCurrentExplicitRemotePreservesLocalHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	seed := Options{RepoPath: filepath.Join(root, "seed"), Remote: remote, Branch: "main"}
	if err := EnsureRepo(ctx, seed); err != nil {
		t.Fatal(err)
	}
	writeCommit := func(opts Options, name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(opts.RepoPath, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if changed, err := Commit(ctx, opts, value); err != nil || !changed {
			t.Fatalf("commit: %v, %v", changed, err)
		}
	}
	writeCommit(seed, "base", "base")
	if err := Push(ctx, seed); err != nil {
		t.Fatal(err)
	}
	opts := Options{RepoPath: filepath.Join(root, "local"), Remote: remote, Branch: "main"}
	if err := PullCurrent(ctx, opts); err != nil {
		t.Fatal(err)
	}
	writeCommit(seed, "remote", "new remote")
	if err := Push(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := PullCurrent(ctx, opts); err != nil {
		t.Fatalf("fast-forward: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.RepoPath, "remote")); err != nil {
		t.Fatal("remote update not pulled")
	}
	writeCommit(opts, "local", "unpublished")
	before, err := output(ctx, opts.RepoPath, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := PullCurrent(ctx, opts); err != nil {
		t.Fatalf("ahead pull: %v", err)
	}
	after, err := output(ctx, opts.RepoPath, "git", "rev-parse", "HEAD")
	if err != nil || after != before {
		t.Fatalf("ahead HEAD changed: %q -> %q, %v", before, after, err)
	}
	writeCommit(seed, "diverged", "divergent remote")
	if err := Push(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := PullCurrent(ctx, opts); err == nil {
		t.Fatal("divergent pull should fail")
	}
	after, err = output(ctx, opts.RepoPath, "git", "rev-parse", "HEAD")
	if err != nil || after != before {
		t.Fatalf("divergent HEAD changed: %q -> %q, %v", before, after, err)
	}
	data, err := os.ReadFile(filepath.Join(opts.RepoPath, "local"))
	if err != nil || strings.TrimSpace(string(data)) != "unpublished" {
		t.Fatalf("local work lost: %q, %v", data, err)
	}
}
