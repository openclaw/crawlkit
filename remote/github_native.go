package remote

import (
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
)

const (
	// GitHubNativeV1 retains the original full native inventory hash semantics.
	GitHubNativeV1 = "gitcrawl-native-v1"
	// GitHubNativeV2 identifies the canonical compact InventoryCodec digest.
	// It does not change the v1 seed contract or advertise receiver support.
	GitHubNativeV2 = "gitcrawl-native-v2"
)

var (
	ErrGitHubNativeAnchor = errors.New("invalid GitHub native anchor")
	ErrGitHubNativeRange  = errors.New("incompatible GitHub native range")
)

// GitHubNativeAnchor is a typed native inventory identity, not a qualified
// baseline or source/privacy authority. SchemaCookie is SQLite's schema cookie,
// not the application's fixed schema version. Version determines hash semantics.
type GitHubNativeAnchor struct {
	Version, Epoch                                    string
	Generation, SchemaCookie, Sequence, PrunedThrough int64
	ScopeSHA256, InventorySHA256                      string
}

// ParseGitHubNativeAnchor accepts only exact eight-field v1/v2 tokens. It never
// trims, normalizes or silently converts an old inventory hash into a new one.
func ParseGitHubNativeAnchor(token string) (GitHubNativeAnchor, error) {
	var zero GitHubNativeAnchor
	if len(token) > maxChangeCursorBytes {
		return zero, ErrGitHubNativeAnchor
	}
	parts := strings.Split(token, ":")
	if len(parts) != 8 {
		return zero, ErrGitHubNativeAnchor
	}
	a := GitHubNativeAnchor{Version: parts[0], Epoch: parts[1], ScopeSHA256: parts[6], InventorySHA256: parts[7]}
	for i, field := range []*int64{&a.Generation, &a.SchemaCookie, &a.Sequence, &a.PrunedThrough} {
		n, err := strconv.ParseInt(parts[i+2], 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != parts[i+2] {
			return zero, ErrGitHubNativeAnchor
		}
		*field = n
	}
	if err := validateGitHubNativeAnchor(a); err != nil {
		return zero, err
	}
	return a, nil
}

// EncodeGitHubNativeAnchor validates all typed fields before emitting one
// canonical token. Invalid input returns an empty string and an error.
func EncodeGitHubNativeAnchor(a GitHubNativeAnchor) (string, error) {
	if err := validateGitHubNativeAnchor(a); err != nil {
		return "", err
	}
	return strings.Join([]string{a.Version, a.Epoch, strconv.FormatInt(a.Generation, 10),
		strconv.FormatInt(a.SchemaCookie, 10), strconv.FormatInt(a.Sequence, 10),
		strconv.FormatInt(a.PrunedThrough, 10), a.ScopeSHA256, a.InventorySHA256}, ":"), nil
}

func validateGitHubNativeAnchor(a GitHubNativeAnchor) error {
	if (a.Version != GitHubNativeV1 && a.Version != GitHubNativeV2) || !seedHex(a.Epoch, 16) ||
		!seedHex(a.ScopeSHA256, sha256.Size) || !seedHex(a.InventorySHA256, sha256.Size) ||
		a.Generation <= 0 || a.SchemaCookie <= 0 || a.Sequence < 0 || a.PrunedThrough < 0 || a.PrunedThrough > a.Sequence {
		return ErrGitHubNativeAnchor
	}
	return nil
}

// ValidateGitHubNativeRange checks a v1/v2 From followed by a v2 Through. The
// source still owns baseline qualification, post-upgrade journal readiness,
// complete interval capture, policy and final receipt settlement.
func ValidateGitHubNativeRange(from, through GitHubNativeAnchor) error {
	if err := validateGitHubNativeAnchor(from); err != nil {
		return err
	}
	if err := validateGitHubNativeAnchor(through); err != nil {
		return err
	}
	if through.Version != GitHubNativeV2 || from.Epoch != through.Epoch || from.Generation != through.Generation ||
		from.SchemaCookie != through.SchemaCookie || from.ScopeSHA256 != through.ScopeSHA256 ||
		through.Sequence < from.Sequence || through.PrunedThrough < from.PrunedThrough || from.Sequence < through.PrunedThrough {
		return ErrGitHubNativeRange
	}
	// A first v1->v2 transition can keep H while changing digest algorithms.
	// Once both are v2, equal H cannot describe different inventory contents.
	if from.Version == GitHubNativeV2 && from.Sequence == through.Sequence && from.InventorySHA256 != through.InventorySHA256 {
		return ErrGitHubNativeRange
	}
	return nil
}
