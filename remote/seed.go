package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

const MaxSeedChunks = 8192

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

// SeedReceipt shares the exact-byte receipt contract. Only a receiver-qualified
// completion receipt can establish a baseline; begin/chunk receipts cannot.
type SeedReceipt = ChangeReceipt

type seedDescriptor struct {
	table string
	SeedChunkInfo
}

func seedBatchID(seedID, suffix string) string { return "seed1:" + seedID + ":" + suffix }
func seedPreviousCursor(seedID string, index int) string {
	if index == 0 {
		return seedBatchID(seedID, "begin")
	}
	return seedBatchID(seedID, strconv.Itoa(index-1))
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

func validateSeedTables(tables []SeedTable, names []string) ([]seedDescriptor, error) {
	if len(tables) != len(names) {
		return nil, errors.New("seed requires all ordered tables, including empty tables")
	}
	var descriptors []seedDescriptor
	for i, table := range tables {
		if table.Name != names[i] || table.Chunks == nil || len(table.Chunks) > MaxSeedChunks-len(descriptors) {
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
			descriptors = append(descriptors, seedDescriptor{table.Name, chunk})
		}
		if table.Rows != strconv.FormatInt(rows, 10) || table.SHA256 != hex.EncodeToString(hash.Sum(nil)) {
			return nil, errors.New("seed table row count or ordered digest hash mismatch")
		}
	}
	return descriptors, nil
}

func validateSeedRows(index int, cursor, seedID string, changes []Change, tables []string) error {
	if index < 0 || index >= MaxSeedChunks || cursor != seedPreviousCursor(seedID, index) {
		return errors.New("invalid seed chunk index/expected cursor")
	}
	if len(changes) == 0 {
		return errors.New("seed chunks must contain rows")
	}
	if err := validateChanges(changes); err != nil {
		return err
	}
	for _, change := range changes {
		if change.Operation != "upsert" || !slices.Contains(tables, change.Table) || change.Table != changes[0].Table {
			return errors.New("seed chunks require upserts from one supported table")
		}
	}
	return nil
}

func seedDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func cloneSeedTables(tables []SeedTable) []SeedTable {
	tables = slices.Clone(tables)
	for i := range tables {
		tables[i].Chunks = slices.Clone(tables[i].Chunks)
	}
	return tables
}
