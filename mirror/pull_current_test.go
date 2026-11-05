package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestCommit(t *testing.T, ctx context.Context, opts Options, name, contents, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(opts.RepoPath, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, opts, message); err != nil || !committed {
		t.Fatalf("commit %q = %v, %v", message, committed, err)
	}
}

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
	writeTestCommit(t, ctx, seed, "base", "base", "base")
	if err := Push(ctx, seed); err != nil {
		t.Fatal(err)
	}
	opts := Options{RepoPath: filepath.Join(root, "local"), Remote: remote, Branch: "main"}
	if err := PullCurrent(ctx, opts); err != nil {
		t.Fatal(err)
	}
	writeTestCommit(t, ctx, seed, "remote", "new remote", "new remote")
	if err := Push(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := PullCurrent(ctx, opts); err != nil {
		t.Fatalf("fast-forward: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.RepoPath, "remote")); err != nil {
		t.Fatal("remote update not pulled")
	}
	writeTestCommit(t, ctx, opts, "local", "unpublished", "unpublished")
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
	writeTestCommit(t, ctx, seed, "diverged", "divergent remote", "divergent remote")
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
