package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	MaxSeedChunks          = 8192
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

type SeedTable struct {
	Name   string          `json:"name"`
	Rows   string          `json:"rows"`
	SHA256 string          `json:"sha256"`
	Chunks []SeedChunkInfo `json:"chunks"`
}

type SeedChunkInfo struct {
	Rows   int    `json:"rows"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
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

// SeedReceipt shares the exact-byte receipt contract. Only a receiver-qualified
// completion receipt can establish a baseline; begin/chunk receipts cannot.
type SeedReceipt = ChangeReceipt

// SlackSeedPlan owns a once-validated immutable manifest and indexed chunk
// descriptors. Reuse it across chunk calls; no whole-manifest scans are needed.
type SlackSeedPlan struct {
	manifest SlackSeedManifest
	digest   string
	chunks   []slackSeedDescriptor
}

type slackSeedDescriptor struct {
	table string
	SeedChunkInfo
}

var slackSeedTables = [...]string{"workspaces", "channels", "users", "messages", "message_files", "message_mentions"}

// Manifest returns caller-owned metadata; changing it cannot change this plan.
func (p *SlackSeedPlan) Manifest() SlackSeedManifest {
	m := p.manifest
	m.Scope.ChannelIDs = slices.Clone(m.Scope.ChannelIDs)
	m.Tables = slices.Clone(m.Tables)
	for i := range m.Tables {
		m.Tables[i].Chunks = slices.Clone(m.Tables[i].Chunks)
	}
	return m
}

func (p *SlackSeedPlan) ManifestSHA256() string { return p.digest }

func seedBatchID(seedID, suffix string) string { return "seed1:" + seedID + ":" + suffix }
func seedPreviousCursor(seedID string, index int) string {
	if index == 0 {
		return seedBatchID(seedID, "begin")
	}
	return seedBatchID(seedID, strconv.Itoa(index-1))
}

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

func encodeSeed(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err == nil && len(body) > MaxChangeBytes {
		err = errors.New("seed body exceeds 4 MiB")
	}
	if err != nil {
		return nil, err
	}
	return body, nil
}

func seedHex(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes && strings.ToLower(value) == value
}

func validateSeedIdentity(contract, app, archive, binding, seedID string) error {
	if contract != SlackSeedContract || app != "slacrawl" {
		return errors.New("unsupported Slack seed contract/app")
	}
	if !changeMetadata(archive, maxChangeIDBytes, false) || !changeMetadata(binding, maxChangeIDBytes, false) || !seedHex(seedID, 16) {
		return errors.New("invalid Slack seed archive/binding/ID")
	}
	return nil
}

func validateSlackSeedManifest(m SlackSeedManifest) ([]slackSeedDescriptor, error) {
	if err := validateSeedIdentity(m.Contract, m.App, m.Archive, m.BindingID, m.SeedID); err != nil {
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
	if len(m.Tables) != len(slackSeedTables) {
		return nil, errors.New("seed requires all six ordered tables, including empty tables")
	}
	var descriptors []slackSeedDescriptor
	for i, table := range m.Tables {
		if table.Name != slackSeedTables[i] || table.Chunks == nil || len(table.Chunks) > MaxSeedChunks-len(descriptors) {
			return nil, errors.New("invalid seed table order/chunks")
		}
		var rows int64
		hash := sha256.New()
		for _, chunk := range table.Chunks {
			if chunk.Rows < 1 || chunk.Rows > MaxChanges || chunk.Bytes < 1 || chunk.Bytes > MaxChangeBytes || !seedHex(chunk.SHA256, sha256.Size) {
				return nil, errors.New("invalid seed chunk rows/bytes/hash")
			}
			// At most 8192 * 500 rows; bounded before addition, even on 32-bit.
			rows += int64(chunk.Rows)
			digest, _ := hex.DecodeString(chunk.SHA256)
			_, _ = hash.Write(digest)
			descriptors = append(descriptors, slackSeedDescriptor{table.Name, chunk})
		}
		if table.Rows != strconv.FormatInt(rows, 10) || table.SHA256 != hex.EncodeToString(hash.Sum(nil)) {
			return nil, errors.New("seed table row count or ordered digest hash mismatch")
		}
		if table.Name == "users" && !m.Scope.IncludeWorkspaceUsers && rows != 0 {
			return nil, errors.New("seed users require explicit scope opt-in")
		}
	}
	return descriptors, nil
}

func validateSlackSeedChunk(c SlackSeedChunk) error {
	if err := validateSeedIdentity(c.Contract, c.App, c.Archive, c.BindingID, c.SeedID); err != nil {
		return err
	}
	if c.Index < 0 || c.Index >= MaxSeedChunks || c.ExpectedCursor != seedPreviousCursor(c.SeedID, c.Index) {
		return errors.New("invalid seed chunk index/expected cursor")
	}
	if len(c.Changes) == 0 {
		return errors.New("seed chunks must contain rows")
	}
	if err := validateChanges(c.Changes); err != nil {
		return err
	}
	for _, change := range c.Changes {
		if change.Operation != "upsert" || !slices.Contains(slackSeedTables[:], change.Table) || change.Table != c.Changes[0].Table {
			return errors.New("seed chunks require upserts from one supported table")
		}
	}
	return nil
}

func validateSlackSeedComplete(c SlackSeedComplete) error {
	if err := validateSeedIdentity(c.Contract, c.App, c.Archive, c.BindingID, c.SeedID); err != nil {
		return err
	}
	if !seedHex(c.ManifestSHA256, sha256.Size) || !changeMetadata(c.ExpectedCursor, maxChangeCursorBytes, false) {
		return errors.New("invalid seed completion manifest hash/cursor")
	}
	return nil
}

func seedDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
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
