package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Options struct {
	RepoPath string
	Remote   string
	Branch   string
	Git      string
}

func EnsureRepo(ctx context.Context, opts Options) error {
	opts = normalize(opts)
	if opts.RepoPath == "" {
		return errors.New("repo path is required")
	}
	if _, err := os.Stat(filepath.Join(opts.RepoPath, ".git")); err == nil {
		if opts.Remote != "" {
			return setOrigin(ctx, opts)
		}
		return nil
	}
	if opts.Remote != "" {
		if err := os.MkdirAll(filepath.Dir(opts.RepoPath), 0o755); err != nil {
			return fmt.Errorf("create repo parent: %w", err)
		}
		if err := run(ctx, "", opts.Git, "clone", opts.Remote, opts.RepoPath); err != nil {
			return err
		}
		if opts.Branch != "" {
			return run(ctx, opts.RepoPath, opts.Git, "checkout", "-B", opts.Branch)
		}
		return nil
	}
	if err := os.MkdirAll(opts.RepoPath, 0o755); err != nil {
		return fmt.Errorf("create repo path: %w", err)
	}
	if err := run(ctx, opts.RepoPath, opts.Git, "init"); err != nil {
		return err
	}
	if opts.Branch != "" {
		return run(ctx, opts.RepoPath, opts.Git, "checkout", "-B", opts.Branch)
	}
	return nil
}

func EnsureRemote(ctx context.Context, opts Options) error {
	opts = normalize(opts)
	if opts.Remote == "" {
		return errors.New("remote is required")
	}
	if err := EnsureRepo(ctx, opts); err != nil {
		return err
	}
	return setOrigin(ctx, opts)
}

func Pull(ctx context.Context, opts Options) error {
	opts = normalize(opts)
	if opts.Remote == "" {
		return EnsureRepo(ctx, opts)
	}
	if err := EnsureRepo(ctx, opts); err != nil {
		return err
	}
	if err := run(ctx, opts.RepoPath, opts.Git, "fetch", "--prune", "origin"); err != nil {
		return err
	}
	remoteRef := "refs/remotes/origin/" + opts.Branch
	if _, err := output(ctx, opts.RepoPath, opts.Git, "rev-parse", "--verify", remoteRef); err != nil {
		return run(ctx, opts.RepoPath, opts.Git, "checkout", "-B", opts.Branch)
	}
	return run(ctx, opts.RepoPath, opts.Git, "checkout", "-B", opts.Branch, "origin/"+opts.Branch)
}

func PullCurrent(ctx context.Context, opts Options) error {
	opts = normalize(opts)
	if opts.Remote != "" {
		return Pull(ctx, opts)
	}
	if err := EnsureRepo(ctx, opts); err != nil {
		return err
	}
	if err := run(ctx, opts.RepoPath, opts.Git, "fetch", "--prune", "origin"); err != nil {
		return err
	}
	if _, err := output(ctx, opts.RepoPath, opts.Git, "rev-parse", "--verify", "refs/heads/"+opts.Branch); err != nil {
		return run(ctx, opts.RepoPath, opts.Git, "checkout", "-B", opts.Branch, "origin/"+opts.Branch)
	}
	if err := run(ctx, opts.RepoPath, opts.Git, "checkout", opts.Branch); err != nil {
		return err
	}
	return run(ctx, opts.RepoPath, opts.Git, "pull", "--ff-only", "origin", opts.Branch)
}

func Commit(ctx context.Context, opts Options, message string) (bool, error) {
	return CommitPaths(ctx, opts, message, []string{"."})
}

func CommitPaths(ctx context.Context, opts Options, message string, paths []string) (bool, error) {
	opts = normalize(opts)
	if message == "" {
		message = "archive: update snapshot"
	}
	pathspecs, err := cleanPathspecs(paths)
	if err != nil {
		return false, err
	}
	if len(pathspecs) == 0 {
		return false, nil
	}
	args := append([]string{"add", "--"}, pathspecs...)
	if err := run(ctx, opts.RepoPath, opts.Git, args...); err != nil {
		return false, err
	}
	staged, err := staged(ctx, opts, pathspecs)
	if err != nil {
		return false, err
	}
	if !staged {
		return false, nil
	}
	commitArgs := []string{
		"-c", "commit.gpgsign=false",
		"-c", "user.name=crawlkit",
		"-c", "user.email=crawlkit@example.invalid",
		"commit", "-m", message,
		"--",
	}
	commitArgs = append(commitArgs, pathspecs...)
	if err := run(ctx, opts.RepoPath, opts.Git, commitArgs...); err != nil {
		return false, err
	}
	return true, nil
}

func Push(ctx context.Context, opts Options) error {
	opts = normalize(opts)
	out, err := output(ctx, opts.RepoPath, opts.Git, "push", "-u", "origin", opts.Branch)
	if err == nil {
		return nil
	}
	if !isNonFastForward(out) {
		return fmt.Errorf("git push: %w\n%s", err, strings.TrimSpace(out))
	}
	if pullErr := run(ctx, opts.RepoPath, opts.Git, "pull", "--rebase", "--autostash", "origin", opts.Branch); pullErr != nil {
		return fmt.Errorf("rebase before push retry: %w", pullErr)
	}
	return run(ctx, opts.RepoPath, opts.Git, "push", "-u", "origin", opts.Branch)
}

func Dirty(ctx context.Context, opts Options) (bool, error) {
	opts = normalize(opts)
	out, err := output(ctx, opts.RepoPath, opts.Git, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func CleanSQLiteSidecars(rootDir string) (int, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return 0, errors.New("root dir is required")
	}
	count := 0
	err := filepath.WalkDir(rootDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSQLiteSidecar(path) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove sqlite sidecar %s: %w", path, err)
		}
		count++
		return nil
	})
	if err != nil {
		return count, fmt.Errorf("clean sqlite sidecars: %w", err)
	}
	return count, nil
}

func normalize(opts Options) Options {
	opts.RepoPath = strings.TrimSpace(opts.RepoPath)
	opts.Remote = strings.TrimSpace(opts.Remote)
	opts.Branch = strings.TrimSpace(opts.Branch)
	opts.Git = strings.TrimSpace(opts.Git)
	if opts.Branch == "" {
		opts.Branch = "main"
	}
	if opts.Git == "" {
		opts.Git = "git"
	}
	return opts
}

func setOrigin(ctx context.Context, opts Options) error {
	current, err := output(ctx, opts.RepoPath, opts.Git, "remote", "get-url", "origin")
	if err != nil {
		return run(ctx, opts.RepoPath, opts.Git, "remote", "add", "origin", opts.Remote)
	}
	if strings.TrimSpace(current) == opts.Remote {
		return nil
	}
	return run(ctx, opts.RepoPath, opts.Git, "remote", "set-url", "origin", opts.Remote)
}

func cleanPathspecs(paths []string) ([]string, error) {
	var out []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if filepath.IsAbs(path) {
			return nil, fmt.Errorf("commit path %q must be relative", path)
		}
		clean := filepath.Clean(path)
		if clean == "." {
			out = append(out, ".")
			continue
		}
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("commit path %q must stay inside the repo", path)
		}
		out = append(out, filepath.ToSlash(clean))
	}
	return out, nil
}

func staged(ctx context.Context, opts Options, pathspecs []string) (bool, error) {
	opts = normalize(opts)
	args := []string{"diff", "--cached", "--quiet", "--"}
	args = append(args, pathspecs...)
	out, err := output(ctx, opts.RepoPath, opts.Git, args...)
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --cached --quiet -- %s: %w\n%s", strings.Join(pathspecs, " "), err, strings.TrimSpace(out))
}

func isSQLiteSidecar(path string) bool {
	name := filepath.Base(path)
	return strings.HasSuffix(name, ".db-wal") ||
		strings.HasSuffix(name, ".db-shm") ||
		strings.HasSuffix(name, ".sqlite-wal") ||
		strings.HasSuffix(name, ".sqlite-shm") ||
		strings.HasSuffix(name, ".sqlite3-wal") ||
		strings.HasSuffix(name, ".sqlite3-shm")
}

func run(ctx context.Context, dir, git string, args ...string) error {
	out, err := output(ctx, dir, git, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", git, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}

func output(ctx context.Context, dir, git string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, git, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func isNonFastForward(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "non-fast-forward") ||
		strings.Contains(lower, "fetch first") ||
		strings.Contains(lower, "failed to push some refs")
}
