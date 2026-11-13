package remote

import (
	"encoding/json"
	"errors"

	"github.com/openclaw/crawlkit/internal/jsontext"
)

func validateSeedTablesJSON(body []byte) error {
	var tables []json.RawMessage
	if err := json.Unmarshal(body, &tables); err != nil {
		return err
	}
	for _, raw := range tables {
		var table SeedTable
		fields, err := decodeChangeObject(raw, "name rows sha256 chunks", "", &table)
		if err != nil {
			return err
		}
		var chunks []json.RawMessage
		if err := json.Unmarshal(fields["chunks"], &chunks); err != nil {
			return err
		}
		for _, raw := range chunks {
			var chunk SeedChunkInfo
			if _, err := decodeChangeObject(raw, "rows bytes sha256", "", &chunk); err != nil {
				return err
			}
		}
	}
	return nil
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
