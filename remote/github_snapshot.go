package remote

import (
	"crypto/sha256"
	"errors"
	"strings"
)

// GitHubSnapshotAnchor binds a retained file and its exported inventory. It
// carries no journal position, freshness claim or permission to apply changes.
type GitHubSnapshotAnchor struct {
	FileSHA256      string
	ScopeSHA256     string
	InventorySHA256 string
}

var ErrGitHubSnapshotAnchor = errors.New("invalid GitHub retained snapshot anchor")

func ParseGitHubSnapshotAnchor(token string) (GitHubSnapshotAnchor, error) {
	parts := strings.Split(token, ":")
	if len(parts) != 4 || parts[0] != "gitcrawl-snapshot-v1" {
		return GitHubSnapshotAnchor{}, ErrGitHubSnapshotAnchor
	}
	anchor := GitHubSnapshotAnchor{FileSHA256: parts[1], ScopeSHA256: parts[2], InventorySHA256: parts[3]}
	if !validGitHubSnapshotAnchor(anchor) {
		return GitHubSnapshotAnchor{}, ErrGitHubSnapshotAnchor
	}
	return anchor, nil
}

func EncodeGitHubSnapshotAnchor(anchor GitHubSnapshotAnchor) (string, error) {
	if !validGitHubSnapshotAnchor(anchor) {
		return "", ErrGitHubSnapshotAnchor
	}
	return "gitcrawl-snapshot-v1:" + anchor.FileSHA256 + ":" + anchor.ScopeSHA256 + ":" + anchor.InventorySHA256, nil
}

func validGitHubSnapshotAnchor(anchor GitHubSnapshotAnchor) bool {
	return seedHex(anchor.FileSHA256, sha256.Size) && seedHex(anchor.ScopeSHA256, sha256.Size) && seedHex(anchor.InventorySHA256, sha256.Size)
}
