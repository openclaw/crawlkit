package remote

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
	if err := validateSeedTablesJSON(fields["tables"]); err != nil {
		return nil, err
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
