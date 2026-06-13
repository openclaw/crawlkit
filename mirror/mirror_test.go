package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureRepoCommitDirty(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "share")
	opts := Options{RepoPath: repo, Branch: "main"}
	if err := EnsureRepo(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	dirty, err := Dirty(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("new repo should be clean")
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	committed, err := Commit(ctx, opts, "archive: test")
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("expected commit")
	}
	dirty, err = Dirty(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("repo should be clean after commit")
	}
}

func TestEnsureRepoUpdatesExistingOrigin(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "share")
	opts := Options{RepoPath: repo, Branch: "main"}
	if err := EnsureRepo(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, repo, "git", "remote", "add", "origin", "https://example.invalid/old.git"); err != nil {
		t.Fatal(err)
	}
	opts.Remote = "https://example.invalid/new.git"
	if err := EnsureRemote(ctx, opts); err != nil {
		t.Fatal(err)
	}
	out, err := output(ctx, repo, "git", "remote", "get-url", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != opts.Remote {
		t.Fatalf("origin = %q, want %q", strings.TrimSpace(out), opts.Remote)
	}
}

func TestPushWritesCurrentBranchToOrigin(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	repo := filepath.Join(dir, "share")
	if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	opts := Options{RepoPath: repo, Remote: remote, Branch: "main"}
	if err := EnsureRemote(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, opts, "archive: push"); err != nil {
		t.Fatal(err)
	} else if !committed {
		t.Fatal("expected commit")
	}
	if err := Push(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := output(ctx, remote, "git", "rev-parse", "--verify", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
}

func TestCommitPathsDoesNotStageUnrelatedFiles(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "share")
	opts := Options{RepoPath: repo, Branch: "main"}
	if err := EnsureRepo(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("local draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	committed, err := CommitPaths(ctx, opts, "archive: manifest", []string{"manifest.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("expected commit")
	}
	tree, err := output(ctx, repo, "git", "ls-tree", "--name-only", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tree, "manifest.json") {
		t.Fatalf("manifest was not committed: %q", tree)
	}
	if strings.Contains(tree, "notes.txt") {
		t.Fatalf("unrelated file was committed: %q", tree)
	}
	status, err := output(ctx, repo, "git", "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(status) != "?? notes.txt" {
		t.Fatalf("status = %q, want only untracked notes.txt", strings.TrimSpace(status))
	}
}

func TestCommitPathsDoesNotCommitPrestagedUnrelatedFiles(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "share")
	opts := Options{RepoPath: repo, Branch: "main"}
	if err := EnsureRepo(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("local draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, repo, "git", "add", "notes.txt"); err != nil {
		t.Fatal(err)
	}
	committed, err := CommitPaths(ctx, opts, "archive: manifest", []string{"manifest.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("expected commit")
	}
	tree, err := output(ctx, repo, "git", "ls-tree", "--name-only", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tree, "notes.txt") {
		t.Fatalf("unrelated file was committed: %q", tree)
	}
	status, err := output(ctx, repo, "git", "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(status) != "A  notes.txt" {
		t.Fatalf("status = %q, want staged notes.txt", strings.TrimSpace(status))
	}
}

func TestPullCurrentUsesExistingOrigin(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "share")
	if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, "", "git", "clone", remote, seed); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, seed, "git", "checkout", "-B", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "manifest.json"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, seed, "git", "add", "manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, seed, "git", "-c", "commit.gpgsign=false", "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "one"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, seed, "git", "push", "-u", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if err := Pull(ctx, Options{RepoPath: repo, Remote: remote, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "manifest.json"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, seed, "git", "add", "manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, seed, "git", "-c", "commit.gpgsign=false", "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "two"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, seed, "git", "push", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if err := PullCurrent(ctx, Options{RepoPath: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(data), "\r\n", "\n") != "two\n" {
		t.Fatalf("manifest = %q, want updated content", data)
	}
}

func TestCleanSQLiteSidecars(t *testing.T) {
	dir := t.TempDir()
	files := []string{"archive.db", "archive.db-wal", "archive.db-shm", "notes.txt"}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := CleanSQLiteSidecars(dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for _, file := range []string{"archive.db-wal", "archive.db-shm"} {
		if _, err := os.Stat(filepath.Join(dir, file)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been removed, err=%v", file, err)
		}
	}
	for _, file := range []string{"archive.db", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Fatalf("%s should remain: %v", file, err)
		}
	}
}
