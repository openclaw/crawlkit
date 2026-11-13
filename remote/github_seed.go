package remote

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
)

const (
	// These optional routes do not advertise receiver support or authorization.
	GitHubSeedContract      = "gitcrawl-seed-v1"
	GitHubSeedsRoute        = "/v1/apps/gitcrawl/archives/:archive/seeds"
	GitHubSeedChunksRoute   = GitHubSeedsRoute + "/:seed_id/chunks/:index"
	GitHubSeedCompleteRoute = GitHubSeedsRoute + "/:seed_id/complete"
)

// GitHubSeedManifest binds native capture evidence to exact seed request bytes.
// It grants no scope, exclusion-policy, publication or baseline authorization.
// NativeSHA256 describes the native inventory, not a transport-body hash.
type GitHubSeedManifest struct {
	Contract      string      `json:"contract"`
	App           string      `json:"app"`
	Archive       string      `json:"archive"`
	BindingID     string      `json:"binding_id"`
	SeedID        string      `json:"seed_id"`
	Profile       string      `json:"profile"`
	NativeSchema  int         `json:"native_schema"`
	NativeThrough string      `json:"native_through"`
	Repository    string      `json:"repository"`
	RepositoryID  string      `json:"repository_id"`
	ScopeSHA256   string      `json:"scope_sha256"`
	NativeSHA256  string      `json:"native_sha256"`
	Tables        []SeedTable `json:"tables"`
}

type GitHubSeedChunk struct {
	Contract       string   `json:"contract"`
	App            string   `json:"app"`
	Archive        string   `json:"archive"`
	BindingID      string   `json:"binding_id"`
	SeedID         string   `json:"seed_id"`
	Index          int      `json:"index"`
	ExpectedCursor string   `json:"expected_cursor"`
	Changes        []Change `json:"changes"`
}

type GitHubSeedComplete struct {
	Contract       string `json:"contract"`
	App            string `json:"app"`
	Archive        string `json:"archive"`
	BindingID      string `json:"binding_id"`
	SeedID         string `json:"seed_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ExpectedCursor string `json:"expected_cursor"`
}

// GitHubSeedPlan owns a once-validated immutable manifest and indexed chunk
// descriptors. Reuse it across chunk calls; no whole-manifest scans are needed.
type GitHubSeedPlan struct {
	manifest GitHubSeedManifest
	digest   string
	chunks   []seedDescriptor
}

var githubSeedTables = [...]string{
	"repositories", "threads", "comments", "thread_child_observation_reservations",
	"thread_child_observation_memberships", "thread_exclusions", "thread_excluded_nodes",
}

// Manifest returns caller-owned metadata; changing it cannot change this plan.
func (p *GitHubSeedPlan) Manifest() GitHubSeedManifest {
	m := p.manifest
	m.Tables = cloneSeedTables(m.Tables)
	return m
}

func (p *GitHubSeedPlan) ManifestSHA256() string { return p.digest }

// EncodeGitHubSeedManifest preserves table/chunk/row order. Persist these
// bytes; transport hashes never use a JSON re-encoding.
func EncodeGitHubSeedManifest(m GitHubSeedManifest) ([]byte, error) {
	if _, err := validateGitHubSeedManifest(m); err != nil {
		return nil, err
	}
	return encodeSeed(m)
}

func EncodeGitHubSeedChunk(chunk GitHubSeedChunk) ([]byte, error) {
	if err := validateGitHubSeedChunk(chunk); err != nil {
		return nil, err
	}
	return encodeSeed(chunk)
}

func EncodeGitHubSeedComplete(complete GitHubSeedComplete) ([]byte, error) {
	if err := validateGitHubSeedComplete(complete); err != nil {
		return nil, err
	}
	return encodeSeed(complete)
}

func validateGitHubSeedIdentity(contract, app, archive, binding, seedID string) error {
	if contract != GitHubSeedContract || app != "gitcrawl" {
		return errors.New("unsupported GitHub seed contract/app")
	}
	if !changeMetadata(archive, maxChangeIDBytes, false) || !changeMetadata(binding, maxChangeIDBytes, false) || !seedHex(seedID, 16) {
		return errors.New("invalid GitHub seed archive/binding/ID")
	}
	return nil
}

func validateGitHubSeedManifest(m GitHubSeedManifest) ([]seedDescriptor, error) {
	if err := validateGitHubSeedIdentity(m.Contract, m.App, m.Archive, m.BindingID, m.SeedID); err != nil {
		return nil, err
	}
	if m.Profile != "gitcrawl-private-report-v1" || m.NativeSchema != 16 {
		return nil, errors.New("unsupported GitHub seed profile/schema")
	}
	parts := strings.Split(m.Repository, "/")
	if !changeMetadata(m.Repository, maxChangeIDBytes, false) || len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		strings.ContainsAny(m.Repository, " \t\r\n\\") || strings.ToLower(m.Repository) != m.Repository {
		return nil, errors.New("seed repository must be an exact lowercase owner/repo")
	}
	id, err := strconv.ParseInt(m.RepositoryID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != m.RepositoryID {
		return nil, errors.New("seed repository ID must be a positive canonical int64")
	}
	if !seedHex(m.ScopeSHA256, sha256.Size) || !seedHex(m.NativeSHA256, sha256.Size) {
		return nil, errors.New("invalid seed scope/native inventory digest")
	}
	// The SQLite schema cookie is not the fixed native schema version. Include
	// inventory identity because untracked native changes can share a head.
	anchor := strings.Split(m.NativeThrough, ":")
	if len(anchor) != 8 || anchor[0] != "gitcrawl-native-v1" || !seedHex(anchor[1], 16) ||
		anchor[6] != m.ScopeSHA256 || anchor[7] != m.NativeSHA256 {
		return nil, errors.New("invalid GitHub seed native anchor")
	}
	var counters [4]int64
	for i := range counters {
		n, err := strconv.ParseInt(anchor[i+2], 10, 64)
		if err != nil || n < 0 || (i < 2 && n == 0) || strconv.FormatInt(n, 10) != anchor[i+2] {
			return nil, errors.New("invalid GitHub seed native counter")
		}
		counters[i] = n
	}
	if counters[3] > counters[2] {
		return nil, errors.New("seed native floor exceeds sequence")
	}
	return validateSeedTables(m.Tables, githubSeedTables[:])
}

func validateGitHubSeedChunk(c GitHubSeedChunk) error {
	if err := validateGitHubSeedIdentity(c.Contract, c.App, c.Archive, c.BindingID, c.SeedID); err != nil {
		return err
	}
	return validateSeedRows(c.Index, c.ExpectedCursor, c.SeedID, c.Changes, githubSeedTables[:])
}

func validateGitHubSeedComplete(c GitHubSeedComplete) error {
	if err := validateGitHubSeedIdentity(c.Contract, c.App, c.Archive, c.BindingID, c.SeedID); err != nil {
		return err
	}
	if !seedHex(c.ManifestSHA256, sha256.Size) || !changeMetadata(c.ExpectedCursor, maxChangeCursorBytes, false) {
		return errors.New("invalid seed completion manifest hash/cursor")
	}
	return nil
}

// ValidateChunk checks one exact body against its indexed manifest descriptor.
// It preserves NULL/duplicate natural keys. Downstream profile validation and
// baseline eligibility must separately qualify those keys, not discard rows.
func (p *GitHubSeedPlan) ValidateChunk(body []byte) (GitHubSeedChunk, error) {
	c, err := DecodeGitHubSeedChunk(body)
	if err != nil {
		return GitHubSeedChunk{}, err
	}
	m := p.manifest
	if c.Archive != m.Archive || c.BindingID != m.BindingID || c.SeedID != m.SeedID || c.Index >= len(p.chunks) {
		return GitHubSeedChunk{}, errors.New("seed chunk does not match manifest identity/index")
	}
	d := p.chunks[c.Index]
	if len(c.Changes) != d.Rows || len(body) != d.Bytes || seedDigest(body) != d.SHA256 || c.Changes[0].Table != d.table {
		return GitHubSeedChunk{}, errors.New("seed chunk does not match manifest table/count/bytes/hash")
	}
	return c, nil
}

func (p *GitHubSeedPlan) ValidateComplete(body []byte) (GitHubSeedComplete, error) {
	c, err := DecodeGitHubSeedComplete(body)
	if err != nil {
		return GitHubSeedComplete{}, err
	}
	m := p.manifest
	if c.Archive != m.Archive || c.BindingID != m.BindingID || c.SeedID != m.SeedID || c.ManifestSHA256 != p.digest || c.ExpectedCursor != seedPreviousCursor(m.SeedID, len(p.chunks)) {
		return GitHubSeedComplete{}, errors.New("seed completion does not match manifest/hash/final cursor")
	}
	return c, nil
}

// BeginGitHubSeed sends the plan's exact persisted manifest bytes. All seed
// methods reuse credential transport and exact receipt validation, with no
// implicit retries, registration, capability advertisement or activation.
func (c *Client) BeginGitHubSeed(ctx context.Context, archive string, p *GitHubSeedPlan, body []byte) (SeedReceipt, error) {
	if p == nil || p.digest == "" || seedDigest(body) != p.digest || p.manifest.Archive != strings.TrimSpace(archive) {
		return SeedReceipt{}, errors.New("seed manifest bytes/route do not match plan")
	}
	return c.sendChangeRequest(ctx, archivePath("gitcrawl", archive, "seeds"), body, "gitcrawl", p.manifest.Archive, p.manifest.BindingID, seedBatchID(p.manifest.SeedID, "begin"))
}

func (c *Client) ApplyGitHubSeedChunk(ctx context.Context, archive string, p *GitHubSeedPlan, body []byte) (SeedReceipt, error) {
	if p == nil || p.digest == "" || p.manifest.Archive != strings.TrimSpace(archive) {
		return SeedReceipt{}, errors.New("seed route does not match plan")
	}
	chunk, err := p.ValidateChunk(body)
	if err != nil {
		return SeedReceipt{}, err
	}
	index := strconv.Itoa(chunk.Index)
	return c.sendChangeRequest(ctx, archivePath("gitcrawl", archive, "seeds/"+chunk.SeedID+"/chunks/"+index), body, chunk.App, chunk.Archive, chunk.BindingID, seedBatchID(chunk.SeedID, index))
}

func (c *Client) CompleteGitHubSeed(ctx context.Context, archive string, p *GitHubSeedPlan, body []byte) (SeedReceipt, error) {
	if p == nil || p.digest == "" || p.manifest.Archive != strings.TrimSpace(archive) {
		return SeedReceipt{}, errors.New("seed route does not match plan")
	}
	complete, err := p.ValidateComplete(body)
	if err != nil {
		return SeedReceipt{}, err
	}
	return c.sendChangeRequest(ctx, archivePath("gitcrawl", archive, "seeds/"+complete.SeedID+"/complete"), body, complete.App, complete.Archive, complete.BindingID, seedBatchID(complete.SeedID, "complete"))
}
