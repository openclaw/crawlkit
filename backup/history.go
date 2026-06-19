package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/openclaw/crawlkit/mirror"
)

const DefaultHistoryLimit = 20

type HistoryEntry struct {
	Ref      string   `json:"ref"`
	Tags     []string `json:"tags,omitempty"`
	Manifest Manifest `json:"manifest"`
}

func History(ctx context.Context, opts mirror.Options, limit int) ([]HistoryEntry, error) {
	if limit == 0 {
		limit = DefaultHistoryLimit
	}
	if err := mirror.Fetch(ctx, opts); err != nil {
		return nil, err
	}
	refs, err := mirror.CommitsChanging(ctx, opts, "manifest.json", limit)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(refs))
	for _, ref := range refs {
		manifest, commit, err := ReadManifestAt(ctx, opts, ref)
		if err != nil {
			return nil, err
		}
		tags, err := mirror.TagsAt(ctx, opts, commit)
		if err != nil {
			return nil, err
		}
		out = append(out, HistoryEntry{Ref: commit, Tags: tags, Manifest: manifest})
	}
	return out, nil
}

func ReadManifestAt(ctx context.Context, opts mirror.Options, ref string) (Manifest, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		manifest, err := ReadManifest(opts.RepoPath)
		return manifest, "", err
	}
	body, commit, err := mirror.ReadFileAt(ctx, opts, ref, "manifest.json")
	if err != nil {
		return Manifest{}, "", err
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return Manifest{}, "", fmt.Errorf("decode backup manifest at %s: %w", mirror.ShortRef(commit), err)
	}
	return manifest, commit, nil
}

func ReadSnapshotAt(ctx context.Context, cfg Config, opts mirror.Options, manifest Manifest, ref string) ([]DecodedShard, string, error) {
	commit, err := mirror.ResolveCommit(ctx, opts, ref)
	if err != nil {
		return nil, "", err
	}
	shards, err := readSnapshotWith(manifest, func(shard ShardEntry) ([]byte, error) {
		clean, err := cleanShardPath(shard.Path)
		if err != nil {
			return nil, err
		}
		ciphertext, resolved, err := mirror.ReadFileAt(ctx, opts, commit, clean)
		if err != nil {
			return nil, err
		}
		if resolved != commit {
			return nil, fmt.Errorf("backup ref changed while reading %s", clean)
		}
		return DecryptShard(ciphertext, cfg.Identity)
	})
	if err != nil {
		return nil, "", err
	}
	return shards, commit, nil
}

func RestoreFilesAt(ctx context.Context, cfg Config, opts mirror.Options, manifest Manifest, ref, targetRoot string) (int, string, error) {
	return RestoreFilesAtUnder(ctx, cfg, opts, manifest, ref, targetRoot, "")
}

func RestoreFilesAtUnder(ctx context.Context, cfg Config, opts mirror.Options, manifest Manifest, ref, targetRoot, requiredPrefix string) (int, string, error) {
	commit, err := mirror.ResolveCommit(ctx, opts, ref)
	if err != nil {
		return 0, "", err
	}
	count, err := restoreFilesWith(ctx, cfg.Identity, manifest, targetRoot, requiredPrefix, func(rel string) (io.ReadCloser, error) {
		clean, err := cleanShardPath(rel)
		if err != nil {
			return nil, err
		}
		ciphertext, resolved, err := mirror.ReadFileAt(ctx, opts, commit, clean)
		if err != nil {
			return nil, err
		}
		if resolved != commit {
			return nil, fmt.Errorf("backup ref changed while reading %s", clean)
		}
		return readEncryptedFileBytes(ciphertext), nil
	})
	if err != nil {
		return 0, "", err
	}
	return count, commit, nil
}
