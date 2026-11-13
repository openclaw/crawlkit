package remote

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strconv"
	"strings"
)

const (
	// These optional routes do not advertise receiver support or authorization.
	SlackSeedContract      = "slacrawl-seed-v1"
	SlackSeedsRoute        = "/v1/apps/slacrawl/archives/:archive/seeds"
	SlackSeedChunksRoute   = SlackSeedsRoute + "/:seed_id/chunks/:index"
	SlackSeedCompleteRoute = SlackSeedsRoute + "/:seed_id/complete"
)

// SlackSeedScope describes the proposed private archive scope, not permission
// to publish it. Source admission and receiver authorization remain downstream.
type SlackSeedScope struct {
	WorkspaceID           string   `json:"workspace_id"`
	ChannelIDs            []string `json:"channel_ids"`
	IncludeWorkspaceUsers bool     `json:"include_workspace_users"`
}

type SlackSeedManifest struct {
	Contract      string         `json:"contract"`
	App           string         `json:"app"`
	Archive       string         `json:"archive"`
	BindingID     string         `json:"binding_id"`
	SeedID        string         `json:"seed_id"`
	Profile       string         `json:"profile"`
	NativeSchema  int            `json:"native_schema"`
	NativeThrough string         `json:"native_through"`
	Scope         SlackSeedScope `json:"scope"`
	Tables        []SeedTable    `json:"tables"`
}

type SlackSeedChunk struct {
	Contract       string   `json:"contract"`
	App            string   `json:"app"`
	Archive        string   `json:"archive"`
	BindingID      string   `json:"binding_id"`
	SeedID         string   `json:"seed_id"`
	Index          int      `json:"index"`
	ExpectedCursor string   `json:"expected_cursor"`
	Changes        []Change `json:"changes"`
}

type SlackSeedComplete struct {
	Contract       string `json:"contract"`
	App            string `json:"app"`
	Archive        string `json:"archive"`
	BindingID      string `json:"binding_id"`
	SeedID         string `json:"seed_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ExpectedCursor string `json:"expected_cursor"`
}

// SlackSeedPlan owns a once-validated immutable manifest and indexed chunk
// descriptors. Reuse it across chunk calls; no whole-manifest scans are needed.
type SlackSeedPlan struct {
	manifest SlackSeedManifest
	digest   string
	chunks   []seedDescriptor
}

var slackSeedTables = [...]string{"workspaces", "channels", "users", "messages", "message_files", "message_mentions"}

// Manifest returns caller-owned metadata; changing it cannot change this plan.
func (p *SlackSeedPlan) Manifest() SlackSeedManifest {
	m := p.manifest
	m.Scope.ChannelIDs = slices.Clone(m.Scope.ChannelIDs)
	m.Tables = cloneSeedTables(m.Tables)
	return m
}

func (p *SlackSeedPlan) ManifestSHA256() string { return p.digest }

// EncodeSlackSeedManifest sorts scope channels in its own copy. Table/chunk/row
// order is preserved. Persist these bytes; hashes never use a JSON re-encoding.
func EncodeSlackSeedManifest(m SlackSeedManifest) ([]byte, error) {
	m.Scope.ChannelIDs = slices.Clone(m.Scope.ChannelIDs)
	slices.Sort(m.Scope.ChannelIDs)
	if _, err := validateSlackSeedManifest(m); err != nil {
		return nil, err
	}
	return encodeSeed(m)
}

func EncodeSlackSeedChunk(chunk SlackSeedChunk) ([]byte, error) {
	if err := validateSlackSeedChunk(chunk); err != nil {
		return nil, err
	}
	return encodeSeed(chunk)
}

func EncodeSlackSeedComplete(complete SlackSeedComplete) ([]byte, error) {
	if err := validateSlackSeedComplete(complete); err != nil {
		return nil, err
	}
	return encodeSeed(complete)
}

func validateSlackSeedIdentity(contract, app, archive, binding, seedID string) error {
	if contract != SlackSeedContract || app != "slacrawl" {
		return errors.New("unsupported Slack seed contract/app")
	}
	if !changeMetadata(archive, maxChangeIDBytes, false) || !changeMetadata(binding, maxChangeIDBytes, false) || !seedHex(seedID, 16) {
		return errors.New("invalid Slack seed archive/binding/ID")
	}
	return nil
}

func validateSlackSeedManifest(m SlackSeedManifest) ([]seedDescriptor, error) {
	if err := validateSlackSeedIdentity(m.Contract, m.App, m.Archive, m.BindingID, m.SeedID); err != nil {
		return nil, err
	}
	if m.Profile != "slacrawl-private-archive-v1" || m.NativeSchema != 9 {
		return nil, errors.New("unsupported Slack seed profile/schema")
	}
	anchor := strings.Split(m.NativeThrough, ":")
	if len(anchor) != 4 || anchor[0] != "slacrawl-native-v1" || !seedHex(anchor[1], 16) || !seedHex(anchor[3], 16) {
		return nil, errors.New("invalid Slack seed native anchor")
	}
	sequence, err := strconv.ParseInt(anchor[2], 10, 64)
	if err != nil || sequence < 0 || strconv.FormatInt(sequence, 10) != anchor[2] {
		return nil, errors.New("seed native sequence must be a nonnegative canonical int64")
	}
	if !changeMetadata(m.Scope.WorkspaceID, maxChangeIDBytes, false) || len(m.Scope.ChannelIDs) == 0 {
		return nil, errors.New("seed workspace and nonempty channel scope are required")
	}
	seen := make(map[string]bool, len(m.Scope.ChannelIDs))
	for _, channel := range m.Scope.ChannelIDs {
		if !changeMetadata(channel, maxChangeIDBytes, false) || seen[channel] {
			return nil, errors.New("seed channels must be valid unique identifiers")
		}
		seen[channel] = true
	}
	descriptors, err := validateSeedTables(m.Tables, slackSeedTables[:])
	if err != nil {
		return nil, err
	}
	if !m.Scope.IncludeWorkspaceUsers && m.Tables[2].Rows != "0" {
		return nil, errors.New("seed users require explicit scope opt-in")
	}
	return descriptors, nil
}

func validateSlackSeedChunk(c SlackSeedChunk) error {
	if err := validateSlackSeedIdentity(c.Contract, c.App, c.Archive, c.BindingID, c.SeedID); err != nil {
		return err
	}
	return validateSeedRows(c.Index, c.ExpectedCursor, c.SeedID, c.Changes, slackSeedTables[:])
}

func validateSlackSeedComplete(c SlackSeedComplete) error {
	if err := validateSlackSeedIdentity(c.Contract, c.App, c.Archive, c.BindingID, c.SeedID); err != nil {
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
func (p *SlackSeedPlan) ValidateChunk(body []byte) (SlackSeedChunk, error) {
	c, err := DecodeSlackSeedChunk(body)
	if err != nil {
		return SlackSeedChunk{}, err
	}
	m := p.manifest
	if c.Archive != m.Archive || c.BindingID != m.BindingID || c.SeedID != m.SeedID || c.Index >= len(p.chunks) {
		return SlackSeedChunk{}, errors.New("seed chunk does not match manifest identity/index")
	}
	d := p.chunks[c.Index]
	if len(c.Changes) != d.Rows || len(body) != d.Bytes || seedDigest(body) != d.SHA256 || c.Changes[0].Table != d.table {
		return SlackSeedChunk{}, errors.New("seed chunk does not match manifest table/count/bytes/hash")
	}
	return c, nil
}

func (p *SlackSeedPlan) ValidateComplete(body []byte) (SlackSeedComplete, error) {
	c, err := DecodeSlackSeedComplete(body)
	if err != nil {
		return SlackSeedComplete{}, err
	}
	m := p.manifest
	if c.Archive != m.Archive || c.BindingID != m.BindingID || c.SeedID != m.SeedID || c.ManifestSHA256 != p.digest || c.ExpectedCursor != seedPreviousCursor(m.SeedID, len(p.chunks)) {
		return SlackSeedComplete{}, errors.New("seed completion does not match manifest/hash/final cursor")
	}
	return c, nil
}

// BeginSlackSeed sends the plan's exact persisted manifest bytes. All seed
// methods reuse credential transport and exact receipt validation, with no
// implicit retries, registration, capability advertisement or activation.
func (c *Client) BeginSlackSeed(ctx context.Context, archive string, p *SlackSeedPlan, body []byte) (SeedReceipt, error) {
	if p == nil || p.digest == "" || seedDigest(body) != p.digest || p.manifest.Archive != strings.TrimSpace(archive) {
		return SeedReceipt{}, errors.New("seed manifest bytes/route do not match plan")
	}
	return c.sendChangeRequest(ctx, archivePath("slacrawl", archive, "seeds"), body, "slacrawl", p.manifest.Archive, p.manifest.BindingID, seedBatchID(p.manifest.SeedID, "begin"))
}

func (c *Client) ApplySlackSeedChunk(ctx context.Context, archive string, p *SlackSeedPlan, body []byte) (SeedReceipt, error) {
	if p == nil || p.digest == "" || p.manifest.Archive != strings.TrimSpace(archive) {
		return SeedReceipt{}, errors.New("seed route does not match plan")
	}
	chunk, err := p.ValidateChunk(body)
	if err != nil {
		return SeedReceipt{}, err
	}
	index := strconv.Itoa(chunk.Index)
	return c.sendChangeRequest(ctx, archivePath("slacrawl", archive, "seeds/"+chunk.SeedID+"/chunks/"+index), body, chunk.App, chunk.Archive, chunk.BindingID, seedBatchID(chunk.SeedID, index))
}

func (c *Client) CompleteSlackSeed(ctx context.Context, archive string, p *SlackSeedPlan, body []byte) (SeedReceipt, error) {
	if p == nil || p.digest == "" || p.manifest.Archive != strings.TrimSpace(archive) {
		return SeedReceipt{}, errors.New("seed route does not match plan")
	}
	complete, err := p.ValidateComplete(body)
	if err != nil {
		return SeedReceipt{}, err
	}
	return c.sendChangeRequest(ctx, archivePath("slacrawl", archive, "seeds/"+complete.SeedID+"/complete"), body, complete.App, complete.Archive, complete.BindingID, seedBatchID(complete.SeedID, "complete"))
}
