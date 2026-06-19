package mirror

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

type TreeFile struct {
	Path   string `json:"path"`
	Object string `json:"object"`
	Size   int64  `json:"size"`
}

// Fetch updates remote refs and tags without changing the current checkout.
func Fetch(ctx context.Context, opts Options) error {
	opts = normalize(opts)
	if err := EnsureRepo(ctx, opts); err != nil {
		return err
	}
	remotes, err := output(ctx, opts.RepoPath, opts.Git, "remote")
	if err != nil {
		return fmt.Errorf("list git remotes: %w", err)
	}
	if !containsField(remotes, "origin") {
		return nil
	}
	return run(ctx, opts.RepoPath, opts.Git, "fetch", "--prune", "--tags", "origin")
}

func ResolveCommit(ctx context.Context, opts Options, ref string) (string, error) {
	opts = normalize(opts)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("git ref is required")
	}
	out, err := output(ctx, opts.RepoPath, opts.Git, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve git ref %q: %w\n%s", ref, err, strings.TrimSpace(out))
	}
	commit := strings.TrimSpace(out)
	if commit == "" {
		return "", fmt.Errorf("git ref %q resolved to an empty commit", ref)
	}
	return commit, nil
}

func ReadFileAt(ctx context.Context, opts Options, ref, filePath string) ([]byte, string, error) {
	opts = normalize(opts)
	commit, err := ResolveCommit(ctx, opts, ref)
	if err != nil {
		return nil, "", err
	}
	clean, err := cleanTreePath(filePath)
	if err != nil {
		return nil, "", err
	}
	out, err := output(ctx, opts.RepoPath, opts.Git, "show", commit+":"+clean)
	if err != nil {
		return nil, "", fmt.Errorf("read %s at %s: %w\n%s", clean, ShortRef(commit), err, strings.TrimSpace(out))
	}
	return []byte(out), commit, nil
}

func CommitsChanging(ctx context.Context, opts Options, filePath string, limit int) ([]string, error) {
	opts = normalize(opts)
	clean, err := cleanTreePath(filePath)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, errors.New("commit limit must be greater than zero")
	}
	out, err := output(ctx, opts.RepoPath, opts.Git, "log", "--all", "--format=%H", "-n", strconv.Itoa(limit), "--", clean)
	if err != nil {
		return nil, fmt.Errorf("list commits changing %s: %w\n%s", clean, err, strings.TrimSpace(out))
	}
	return strings.Fields(out), nil
}

func TagsAt(ctx context.Context, opts Options, ref string) ([]string, error) {
	opts = normalize(opts)
	commit, err := ResolveCommit(ctx, opts, ref)
	if err != nil {
		return nil, err
	}
	out, err := output(ctx, opts.RepoPath, opts.Git, "tag", "--points-at", commit)
	if err != nil {
		return nil, fmt.Errorf("list tags at %s: %w\n%s", ShortRef(commit), err, strings.TrimSpace(out))
	}
	tags := strings.Fields(out)
	sort.Strings(tags)
	return tags, nil
}

func CreateImmutableTag(ctx context.Context, opts Options, tag string) (string, error) {
	opts = normalize(opts)
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", nil
	}
	if err := ValidateTag(ctx, opts, tag); err != nil {
		return "", err
	}
	fullRef := "refs/tags/" + tag
	head, err := ResolveCommit(ctx, opts, "HEAD")
	if err != nil {
		return "", err
	}
	existing, existingErr := ResolveCommit(ctx, opts, fullRef)
	if existingErr == nil {
		if existing != head {
			return "", fmt.Errorf("snapshot tag %q already points to %s", tag, ShortRef(existing))
		}
		return tag, nil
	}
	if err := run(ctx, opts.RepoPath, opts.Git, "update-ref", fullRef, head); err != nil {
		return "", err
	}
	return tag, nil
}

// ValidateTag checks a proposed tag without changing repository refs.
func ValidateTag(ctx context.Context, opts Options, tag string) error {
	opts = normalize(opts)
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil
	}
	if err := run(ctx, opts.RepoPath, opts.Git, "check-ref-format", "refs/tags/"+tag); err != nil {
		return fmt.Errorf("invalid snapshot tag %q: %w", tag, err)
	}
	return nil
}

func PushAtomic(ctx context.Context, opts Options, refs ...string) error {
	opts = normalize(opts)
	args := []string{"push", "--atomic", "-u", "origin"}
	if len(refs) == 0 {
		refs = []string{"HEAD:refs/heads/" + opts.Branch}
	}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		args = append(args, ref)
	}
	if len(args) == 4 {
		return errors.New("at least one git ref is required")
	}
	return run(ctx, opts.RepoPath, opts.Git, args...)
}

func ListTreeFiles(ctx context.Context, opts Options, ref, root string) ([]TreeFile, error) {
	opts = normalize(opts)
	commit, err := ResolveCommit(ctx, opts, ref)
	if err != nil {
		return nil, err
	}
	args := []string{"ls-tree", "-r", "-l", "-z", commit}
	if strings.TrimSpace(root) != "" {
		clean, err := cleanTreePath(root)
		if err != nil {
			return nil, err
		}
		args = append(args, "--", clean)
	}
	out, err := output(ctx, opts.RepoPath, opts.Git, args...)
	if err != nil {
		return nil, fmt.Errorf("list tree at %s: %w\n%s", ShortRef(commit), err, strings.TrimSpace(out))
	}
	var files []TreeFile
	for record := range strings.SplitSeq(out, "\x00") {
		metadata, filePath, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse git tree record %q", metadata)
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse git tree size %q: %w", fields[3], err)
		}
		files = append(files, TreeFile{Path: filePath, Object: fields[2], Size: size})
	}
	return files, nil
}

func ShortRef(ref string) string {
	if len(ref) > 12 {
		return ref[:12]
	}
	return ref
}

func cleanTreePath(value string) (string, error) {
	clean := path.Clean(strings.TrimSpace(value))
	if clean == "." || clean == ".." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") || strings.ContainsRune(clean, '\x00') {
		return "", fmt.Errorf("invalid git tree path %q", value)
	}
	return clean, nil
}

func containsField(value, target string) bool {
	for _, field := range strings.Fields(value) {
		if field == target {
			return true
		}
	}
	return false
}
