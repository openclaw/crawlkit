package mirror

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

func TestEnsureRepoAppliesPrivateDirectoryMode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "share")
	if err := EnsureRemote(ctx, Options{RepoPath: repo, Remote: remote, Branch: "main", DirMode: 0o750}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o750 {
		t.Fatalf("repo mode = %o, want 750", mode)
	}
	localRepo := filepath.Join(dir, "local-share")
	if err := os.Mkdir(localRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRepo(ctx, Options{RepoPath: localRepo, Branch: "main", DirMode: 0o750}); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(localRepo)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o750 {
		t.Fatalf("local repo mode = %o, want 750", mode)
	}
}

func TestEnsureRepoClonesRequestedRemoteBranch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	remote := filepath.Join(dir, "remote.git")
	repo := filepath.Join(dir, "share")
	if err := run(ctx, "", "git", "init", "-b", "main", seed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "manifest.json"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, Options{RepoPath: seed, Branch: "main"}, "release"); err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	if err := run(ctx, seed, "git", "branch", "release"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "manifest.json"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, Options{RepoPath: seed, Branch: "main"}, "main"); err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	if err := run(ctx, "", "git", "clone", "--bare", seed, remote); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRepo(ctx, Options{RepoPath: repo, Remote: remote, Branch: "release"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(repo, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(body), "\r\n", "\n") != "release\n" {
		t.Fatalf("manifest = %q, want release", body)
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

func TestPullCurrentPreservesUnpublishedLocalBranch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	repo := filepath.Join(dir, "share")
	if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	opts := Options{RepoPath: repo, Remote: remote, Branch: "private"}
	if err := EnsureRemote(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, opts, "local snapshot"); err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	headBefore, err := output(ctx, repo, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := PullCurrent(ctx, Options{RepoPath: repo, Branch: "private"}); err != nil {
		t.Fatal(err)
	}
	headAfter, err := output(ctx, repo, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(headAfter) != strings.TrimSpace(headBefore) {
		t.Fatalf("HEAD moved from %s to %s", strings.TrimSpace(headBefore), strings.TrimSpace(headAfter))
	}
}

func TestPullCurrentRejectsDeletedTrackedBranch(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(seed, "manifest.json"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedOpts := Options{RepoPath: seed, Remote: remote, Branch: "main"}
	if committed, err := Commit(ctx, seedOpts, "remote snapshot"); err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	if err := Push(ctx, seedOpts); err != nil {
		t.Fatal(err)
	}
	if err := Pull(ctx, Options{RepoPath: repo, Remote: remote, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, repo, "git", "branch", "--set-upstream-to", "origin/main", "main"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, remote, "git", "update-ref", "-d", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	err := PullCurrent(ctx, Options{RepoPath: repo, Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "tracked remote branch origin/main is missing") {
		t.Fatalf("PullCurrent error = %v", err)
	}
}

func TestSyncForWriteRebasesUnpushedCommit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "share")
	peer := filepath.Join(dir, "peer")
	if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	opts := Options{RepoPath: seed, Remote: remote, Branch: "main"}
	if err := EnsureRemote(ctx, opts); err != nil {
		t.Fatal(err)
	}
	writeTestCommit(t, ctx, opts, "manifest.json", "one\n", "archive: seed")
	if err := Push(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, "", "git", "clone", "--branch", "main", remote, repo); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, "", "git", "clone", "--branch", "main", remote, peer); err != nil {
		t.Fatal(err)
	}
	localOpts := Options{RepoPath: repo, Branch: "main"}
	writeTestCommit(t, ctx, localOpts, "local.txt", "local\n", "archive: local")
	if tag, err := CreateImmutableTag(ctx, localOpts, "snapshot/local"); err != nil || tag != "snapshot/local" {
		t.Fatalf("local tag = %q, %v", tag, err)
	}
	oldFirst, err := ResolveCommit(ctx, localOpts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeTestCommit(t, ctx, localOpts, "local-two.txt", "local two\n", "archive: local two")
	if tag, err := CreateImmutableTag(ctx, localOpts, "snapshot/local-two"); err != nil || tag != "snapshot/local-two" {
		t.Fatalf("second local tag = %q, %v", tag, err)
	}
	oldLocal, err := ResolveCommit(ctx, localOpts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "local.txt"), []byte("unrelated dirty edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	peerOpts := Options{RepoPath: peer, Branch: "main"}
	writeTestCommit(t, ctx, peerOpts, "remote.txt", "remote\n", "archive: remote")
	if err := Push(ctx, peerOpts); err != nil {
		t.Fatal(err)
	}
	if err := SyncForWrite(ctx, localOpts); err != nil {
		t.Fatal(err)
	}
	newLocal, err := ResolveCommit(ctx, localOpts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if newLocal == oldLocal {
		t.Fatal("local commit was not rebased")
	}
	firstTagged, err := ResolveCommit(ctx, localOpts, "snapshot/local")
	if err != nil {
		t.Fatal(err)
	}
	if firstTagged == oldFirst || firstTagged == newLocal {
		t.Fatalf("first local tag was not mapped to its rebased commit: %s", firstTagged)
	}
	secondTagged, err := ResolveCommit(ctx, localOpts, "snapshot/local-two")
	if err != nil {
		t.Fatal(err)
	}
	if secondTagged != newLocal {
		t.Fatalf("second local tag = %s, want rebased HEAD %s", secondTagged, newLocal)
	}
	for _, name := range []string{"local.txt", "remote.txt"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatalf("%s missing after sync: %v", name, err)
		}
	}
	dirtyBody, err := os.ReadFile(filepath.Join(repo, "local.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(dirtyBody), "\r\n", "\n") != "unrelated dirty edit\n" {
		t.Fatalf("dirty edit was not restored: %q", dirtyBody)
	}
}

func TestSyncForWriteRetargetsTagsBeforeConflictedStashRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "share")
	peer := filepath.Join(dir, "peer")
	if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	seedOpts := Options{RepoPath: seed, Remote: remote, Branch: "main"}
	if err := EnsureRemote(ctx, seedOpts); err != nil {
		t.Fatal(err)
	}
	writeTestCommit(t, ctx, seedOpts, "manifest.json", "base\n", "seed")
	if err := Push(ctx, seedOpts); err != nil {
		t.Fatal(err)
	}
	for _, clone := range []string{repo, peer} {
		if err := run(ctx, "", "git", "clone", "--branch", "main", remote, clone); err != nil {
			t.Fatal(err)
		}
	}
	localOpts := Options{RepoPath: repo, Branch: "main"}
	writeTestCommit(t, ctx, localOpts, "local.txt", "local\n", "local snapshot")
	if tag, err := CreateImmutableTag(ctx, localOpts, "snapshot/conflict"); err != nil || tag != "snapshot/conflict" {
		t.Fatalf("local tag = %q, %v", tag, err)
	}
	oldTag, err := ResolveCommit(ctx, localOpts, "snapshot/conflict")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("dirty local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	peerOpts := Options{RepoPath: peer, Branch: "main"}
	writeTestCommit(t, ctx, peerOpts, "manifest.json", "remote update\n", "remote update")
	if err := Push(ctx, peerOpts); err != nil {
		t.Fatal(err)
	}
	if err := SyncForWrite(ctx, localOpts); err == nil || !strings.Contains(err.Error(), "restore local changes") {
		t.Fatalf("SyncForWrite error = %v", err)
	}
	newTag, err := ResolveCommit(ctx, localOpts, "snapshot/conflict")
	if err != nil {
		t.Fatal(err)
	}
	head, err := ResolveCommit(ctx, localOpts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if newTag == oldTag || newTag != head {
		t.Fatalf("tag after conflicted restore = %s, old %s, HEAD %s", newTag, oldTag, head)
	}
}

func TestPushSnapshotRebasesAndRetargetsTag(t *testing.T) {
	for _, tc := range []struct {
		name string
		push func(context.Context, Options, string) error
	}{
		{"configured branch", PushSnapshot},
		{"current branch", PushCurrentSnapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			remote := filepath.Join(dir, "remote.git")
			seed := filepath.Join(dir, "seed")
			publisher := filepath.Join(dir, "publisher")
			peer := filepath.Join(dir, "peer")
			if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
				t.Fatal(err)
			}
			seedOpts := Options{RepoPath: seed, Remote: remote, Branch: "main"}
			if err := EnsureRemote(ctx, seedOpts); err != nil {
				t.Fatal(err)
			}
			writeTestCommit(t, ctx, seedOpts, "manifest.json", "seed\n", "seed")
			if err := Push(ctx, seedOpts); err != nil {
				t.Fatal(err)
			}
			for _, clone := range []string{publisher, peer} {
				if err := run(ctx, "", "git", "clone", "--branch", "main", remote, clone); err != nil {
					t.Fatal(err)
				}
			}
			publisherOpts := Options{RepoPath: publisher, Branch: "main"}
			writeTestCommit(t, ctx, publisherOpts, "manifest.json", "publisher\n", "publisher snapshot")
			if tag, err := CreateImmutableTag(ctx, publisherOpts, "snapshot/race"); err != nil || tag != "snapshot/race" {
				t.Fatalf("publisher tag = %q, %v", tag, err)
			}
			peerOpts := Options{RepoPath: peer, Branch: "main"}
			writeTestCommit(t, ctx, peerOpts, "peer.txt", "peer\n", "peer update")
			if err := Push(ctx, peerOpts); err != nil {
				t.Fatal(err)
			}
			if err := tc.push(ctx, publisherOpts, "snapshot/race"); err != nil {
				t.Fatal(err)
			}
			head, err := ResolveCommit(ctx, publisherOpts, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			tagged, err := ResolveCommit(ctx, publisherOpts, "snapshot/race")
			if err != nil {
				t.Fatal(err)
			}
			if tagged != head {
				t.Fatalf("snapshot tag = %s, want rebased HEAD %s", tagged, head)
			}
			remoteHead, err := ResolveCommit(ctx, Options{RepoPath: remote}, "refs/heads/main")
			if err != nil {
				t.Fatal(err)
			}
			remoteTag, err := ResolveCommit(ctx, Options{RepoPath: remote}, "refs/tags/snapshot/race")
			if err != nil {
				t.Fatal(err)
			}
			if remoteHead != head || remoteTag != head {
				t.Fatalf("remote branch/tag = %s/%s, want %s", remoteHead, remoteTag, head)
			}

		})
	}
}

func TestSyncCurrentForWritePreservesLegacyBranch(t *testing.T) {
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
	if err := run(ctx, seed, "git", "checkout", "-B", "legacy"); err != nil {
		t.Fatal(err)
	}
	seedOpts := Options{RepoPath: seed, Branch: "legacy"}
	writeTestCommit(t, ctx, seedOpts, "manifest.json", "one\n", "archive: seed")
	if err := PushAtomic(ctx, seedOpts, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, "", "git", "clone", "--branch", "legacy", remote, repo); err != nil {
		t.Fatal(err)
	}
	writeTestCommit(t, ctx, seedOpts, "remote.txt", "remote\n", "archive: remote")
	if err := PushAtomic(ctx, seedOpts, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := SyncCurrentForWrite(ctx, Options{RepoPath: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "remote.txt")); err != nil {
		t.Fatalf("legacy branch did not sync: %v", err)
	}
	branch, err := output(ctx, repo, "git", "branch", "--show-current")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(branch) != "legacy" {
		t.Fatalf("branch = %q, want legacy", strings.TrimSpace(branch))
	}
	localOpts := Options{RepoPath: repo, Branch: "main"}
	writeTestCommit(t, ctx, localOpts, "local-snapshot.txt", "local\n", "archive: local snapshot")
	if tag, err := CreateImmutableTag(ctx, localOpts, "snapshot/legacy"); err != nil || tag != "snapshot/legacy" {
		t.Fatalf("legacy snapshot tag = %q, %v", tag, err)
	}
	writeTestCommit(t, ctx, seedOpts, "remote-two.txt", "remote two\n", "archive: remote two")
	if err := PushAtomic(ctx, seedOpts, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := PushCurrentSnapshot(ctx, localOpts, "snapshot/legacy"); err != nil {
		t.Fatal(err)
	}
	localHead, err := ResolveCommit(ctx, localOpts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	remoteHead, err := ResolveCommit(ctx, Options{RepoPath: remote}, "refs/heads/legacy")
	if err != nil {
		t.Fatal(err)
	}
	remoteTag, err := ResolveCommit(ctx, Options{RepoPath: remote}, "refs/tags/snapshot/legacy")
	if err != nil {
		t.Fatal(err)
	}
	if remoteHead != localHead || remoteTag != localHead {
		t.Fatalf("legacy remote branch/tag = %s/%s, want %s", remoteHead, remoteTag, localHead)
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

func TestHistoryReadsAndTagsWithoutChangingCheckout(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	repo := filepath.Join(dir, "share")
	peer := filepath.Join(dir, "peer")
	if err := run(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	opts := Options{RepoPath: repo, Remote: remote, Branch: "main"}
	if err := EnsureRemote(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "Meeting notes.md"), []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, opts, "archive: one"); err != nil || !committed {
		t.Fatalf("first commit = %v, %v", committed, err)
	}
	first, err := ResolveCommit(ctx, opts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if tag, err := CreateImmutableTag(ctx, opts, "snapshot/one"); err != nil || tag != "snapshot/one" {
		t.Fatalf("tag = %q, %v", tag, err)
	}
	if err := PushAtomic(ctx, opts, "HEAD:refs/heads/main", "refs/tags/snapshot/one"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, opts, "archive: two"); err != nil || !committed {
		t.Fatalf("second commit = %v, %v", committed, err)
	}
	second, err := ResolveCommit(ctx, opts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("second commit did not advance")
	}
	if err := Push(ctx, opts); err != nil {
		t.Fatal(err)
	}

	if err := run(ctx, "", "git", "clone", remote, peer); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, peer, "git", "checkout", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peer, "note.txt"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, peer, "git", "add", "note.txt"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, peer, "git", "-c", "commit.gpgsign=false", "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "remote"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, peer, "git", "push", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(ctx, opts); err != nil {
		t.Fatal(err)
	}
	afterFetch, err := ResolveCommit(ctx, opts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if afterFetch != second {
		t.Fatalf("fetch changed checkout from %s to %s", second, afterFetch)
	}

	body, resolved, err := ReadFileAt(ctx, opts, "snapshot/one", "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "one\n" || resolved != first {
		t.Fatalf("historical file = %q at %s", body, resolved)
	}
	commits, err := CommitsChanging(ctx, opts, "manifest.json", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0] != second || commits[1] != first {
		t.Fatalf("commits = %#v", commits)
	}
	tags, err := TagsAt(ctx, opts, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0] != "snapshot/one" {
		t.Fatalf("tags = %#v", tags)
	}
	files, err := ListTreeFiles(ctx, opts, first, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "Meeting notes.md" || files[0].Size != 6 || files[1].Path != "manifest.json" || files[1].Size != 4 {
		t.Fatalf("tree files = %#v", files)
	}
	if _, err := CreateImmutableTag(ctx, opts, "snapshot/one"); err == nil {
		t.Fatal("moving an immutable tag should fail")
	}
}

func TestHistoryValidationAndLocalFetch(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "local")
	opts := Options{RepoPath: repo}
	if err := Fetch(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCommit(ctx, opts, ""); err == nil || err.Error() != "git ref is required" {
		t.Fatalf("empty ref error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, opts, "archive: validation"); err != nil || !committed {
		t.Fatalf("validation commit = %v, %v", committed, err)
	}
	if _, _, err := ReadFileAt(ctx, opts, "HEAD", "../secret"); err == nil || err.Error() != `invalid git tree path "../secret"` {
		t.Fatalf("escaping tree path error = %v", err)
	}
	if _, err := CommitsChanging(ctx, opts, "manifest.json", 0); err == nil || err.Error() != "commit limit must be greater than zero" {
		t.Fatalf("zero history limit error = %v", err)
	}
	if _, err := CreateImmutableTag(ctx, opts, "bad tag"); err == nil || !strings.HasPrefix(err.Error(), `invalid snapshot tag "bad tag": `) {
		t.Fatalf("invalid tag error = %v", err)
	}
	if err := ValidateTag(ctx, opts, "bad tag"); err == nil || !strings.HasPrefix(err.Error(), `invalid snapshot tag "bad tag": `) {
		t.Fatalf("invalid tag validation error = %v", err)
	}
	if err := PushSnapshot(ctx, opts, "snapshot/good:refs/heads/release"); err == nil || !strings.HasPrefix(err.Error(), `invalid snapshot tag "snapshot/good:refs/heads/release": `) {
		t.Fatalf("snapshot refspec error = %v", err)
	}
	if err := PushCurrentSnapshot(ctx, opts, "snapshot/good:refs/heads/release"); err == nil || !strings.HasPrefix(err.Error(), `invalid snapshot tag "snapshot/good:refs/heads/release": `) {
		t.Fatalf("current snapshot refspec error = %v", err)
	}
	if err := ValidateTag(ctx, opts, ""); err != nil {
		t.Fatalf("empty optional tag: %v", err)
	}
	if got := ShortRef("123456789012345"); got != "123456789012" {
		t.Fatalf("short ref = %q", got)
	}
}

func TestHistoryPreservesWhitespacePaths(t *testing.T) {
	ctx := context.Background()
	opts := Options{RepoPath: t.TempDir()}
	if err := EnsureRepo(ctx, opts); err != nil {
		t.Fatal(err)
	}
	names := []string{"note.txt", " note.txt"}
	if runtime.GOOS != "windows" {
		// Win32 aliases trailing spaces instead of storing distinct filenames.
		names = append(names, "note.txt ", " ")
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(opts.RepoPath, name), []byte("contents: "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if committed, err := Commit(ctx, opts, "archive: literal names"); err != nil || !committed {
		t.Fatalf("initial commit = %v, %v", committed, err)
	}
	first, err := ResolveCommit(ctx, opts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.RepoPath, "note.txt"), []byte("changed trimmed name"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := Commit(ctx, opts, "archive: trimmed name"); err != nil || !committed {
		t.Fatalf("second commit = %v, %v", committed, err)
	}
	for _, name := range names[1:] {
		t.Run(name, func(t *testing.T) {
			body, _, err := ReadFileAt(ctx, opts, "HEAD", name)
			if err != nil || string(body) != "contents: "+name {
				t.Errorf("read %q = %q, %v", name, body, err)
			}
			commits, err := CommitsChanging(ctx, opts, name, 10)
			if err != nil || len(commits) != 1 || commits[0] != first {
				t.Errorf("commits for %q = %v, %v; want initial commit only", name, commits, err)
			}
			files, err := ListTreeFiles(ctx, opts, "HEAD", name)
			if err != nil || len(files) != 1 || files[0].Path != name {
				t.Errorf("tree for %q = %v, %v", name, files, err)
			}
		})
	}
}
