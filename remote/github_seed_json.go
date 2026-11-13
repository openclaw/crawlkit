package remote

// DecodeGitHubSeedManifest validates the exact body once. The resulting plan
// owns its metadata and hashes; it does not retain or mutate the caller's bytes.
func DecodeGitHubSeedManifest(body []byte) (*GitHubSeedPlan, error) {
	var m GitHubSeedManifest
	fields, err := decodeSeedObject(body, "contract app archive binding_id seed_id profile native_schema native_through repository repository_id scope_sha256 native_sha256 tables", &m)
	if err != nil {
		return nil, err
	}
	if err := validateSeedTablesJSON(fields["tables"]); err != nil {
		return nil, err
	}
	chunks, err := validateGitHubSeedManifest(m)
	if err != nil {
		return nil, err
	}
	return &GitHubSeedPlan{manifest: m, digest: seedDigest(body), chunks: chunks}, nil
}

// DecodeGitHubSeedChunk validates syntax, typed scalars and the chunk envelope.
// Use the manifest plan's ValidateChunk to verify its descriptor before effects.
func DecodeGitHubSeedChunk(body []byte) (GitHubSeedChunk, error) {
	var c GitHubSeedChunk
	fields, err := decodeSeedObject(body, "contract app archive binding_id seed_id index expected_cursor changes", &c)
	if err != nil {
		return GitHubSeedChunk{}, err
	}
	if err := validateChangesJSON(fields["changes"]); err != nil {
		return GitHubSeedChunk{}, err
	}
	if err := validateGitHubSeedChunk(c); err != nil {
		return GitHubSeedChunk{}, err
	}
	return c, nil
}

// DecodeGitHubSeedComplete validates its envelope; the manifest plan separately
// checks the exact manifest hash and expected final cursor.
func DecodeGitHubSeedComplete(body []byte) (GitHubSeedComplete, error) {
	var c GitHubSeedComplete
	if _, err := decodeSeedObject(body, "contract app archive binding_id seed_id manifest_sha256 expected_cursor", &c); err != nil {
		return GitHubSeedComplete{}, err
	}
	if err := validateGitHubSeedComplete(c); err != nil {
		return GitHubSeedComplete{}, err
	}
	return c, nil
}
