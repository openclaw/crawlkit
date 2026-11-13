package remote

import (
	"encoding/json"
	"errors"

	"github.com/openclaw/crawlkit/internal/jsontext"
)

// DecodeSlackSeedManifest validates the exact body once. The resulting plan
// owns its metadata and hashes; it does not retain or mutate the caller's bytes.
func DecodeSlackSeedManifest(body []byte) (*SlackSeedPlan, error) {
	var m SlackSeedManifest
	fields, err := decodeSeedObject(body, "contract app archive binding_id seed_id profile native_schema native_through scope tables", &m)
	if err != nil {
		return nil, err
	}
	if _, err := decodeChangeObject(fields["scope"], "workspace_id channel_ids include_workspace_users", "", &m.Scope); err != nil {
		return nil, err
	}
	var tables []json.RawMessage
	if err := json.Unmarshal(fields["tables"], &tables); err != nil {
		return nil, err
	}
	for _, raw := range tables {
		var table SeedTable
		fields, err := decodeChangeObject(raw, "name rows sha256 chunks", "", &table)
		if err != nil {
			return nil, err
		}
		var chunks []json.RawMessage
		if err := json.Unmarshal(fields["chunks"], &chunks); err != nil {
			return nil, err
		}
		for _, raw := range chunks {
			var chunk SeedChunkInfo
			if _, err := decodeChangeObject(raw, "rows bytes sha256", "", &chunk); err != nil {
				return nil, err
			}
		}
	}
	chunks, err := validateSlackSeedManifest(m)
	if err != nil {
		return nil, err
	}
	return &SlackSeedPlan{manifest: m, digest: seedDigest(body), chunks: chunks}, nil
}

// DecodeSlackSeedChunk validates syntax, typed scalars and the chunk envelope.
// Use the manifest plan's ValidateChunk to verify its descriptor before effects.
func DecodeSlackSeedChunk(body []byte) (SlackSeedChunk, error) {
	var c SlackSeedChunk
	fields, err := decodeSeedObject(body, "contract app archive binding_id seed_id index expected_cursor changes", &c)
	if err != nil {
		return SlackSeedChunk{}, err
	}
	if err := validateChangesJSON(fields["changes"]); err != nil {
		return SlackSeedChunk{}, err
	}
	if err := validateSlackSeedChunk(c); err != nil {
		return SlackSeedChunk{}, err
	}
	return c, nil
}

// DecodeSlackSeedComplete validates its envelope; the manifest plan separately
// checks the exact manifest hash and expected final cursor.
func DecodeSlackSeedComplete(body []byte) (SlackSeedComplete, error) {
	var c SlackSeedComplete
	if _, err := decodeSeedObject(body, "contract app archive binding_id seed_id manifest_sha256 expected_cursor", &c); err != nil {
		return SlackSeedComplete{}, err
	}
	if err := validateSlackSeedComplete(c); err != nil {
		return SlackSeedComplete{}, err
	}
	return c, nil
}

func decodeSeedObject(body []byte, required string, target any) (map[string]json.RawMessage, error) {
	if len(body) > MaxChangeBytes {
		return nil, errors.New("seed body exceeds 4 MiB")
	}
	fields, err := decodeChangeObject(body, required, "", target)
	if err != nil {
		return nil, err
	}
	// The shared decoder established syntax before the text-only validator.
	if err := jsontext.Validate(body); err != nil {
		return nil, err
	}
	return fields, nil
}
