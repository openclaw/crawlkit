package remote

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

func prepareSQLiteBundleManifest(app, archive string, manifest SQLiteBundleManifest) (SQLiteBundleManifest, []byte, error) {
	app = strings.TrimSpace(app)
	archive = strings.TrimSpace(archive)
	if strings.TrimSpace(manifest.App) == "" {
		manifest.App = app
	}
	if strings.TrimSpace(manifest.Archive) == "" {
		manifest.Archive = archive
	}
	snapshotScoped := manifest.SnapshotID != ""
	if snapshotScoped {
		if err := validateSQLiteBundleManifest(manifest, app, archive); err != nil {
			return SQLiteBundleManifest{}, nil, err
		}
	}
	maxBodySize := sqliteBundleManifestSizeLimit(snapshotScoped)
	encodedSize, err := preflightSQLiteBundleManifestEncoding(manifest, maxBodySize)
	if err != nil {
		return SQLiteBundleManifest{}, nil, err
	}
	var buf bytes.Buffer
	buf.Grow(int(encodedSize) + 1)
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return SQLiteBundleManifest{}, nil, fmt.Errorf("encode sqlite bundle manifest: %w", err)
	}
	body := buf.Bytes()
	if len(body) > 0 && body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}
	if int64(len(body)) != encodedSize {
		return SQLiteBundleManifest{}, nil, errors.New(
			"sqlite bundle manifest encoding size changed after preflight",
		)
	}
	if err := validateSQLiteBundleManifestSize(int64(len(body)), snapshotScoped); err != nil {
		return SQLiteBundleManifest{}, nil, err
	}
	return manifest, body, nil
}

func sqliteBundleManifestSizeLimit(snapshotScoped bool) int64 {
	if snapshotScoped {
		return maxSQLiteSnapshotBundleManifestBytes
	}
	return maxSQLiteMutableBundleManifestBytes
}

func validateSQLiteBundleManifestSize(size int64, snapshotScoped bool) error {
	maxBodySize := sqliteBundleManifestSizeLimit(snapshotScoped)
	if size > maxBodySize {
		return fmt.Errorf("sqlite bundle manifest must not exceed %d bytes", maxBodySize)
	}
	return nil
}

const maxSQLiteBundleManifestJSONDepth = 1000

var (
	jsonMarshalerType    = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType    = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	jsonNumberType       = reflect.TypeOf(json.Number(""))
	errManifestSizeLimit = errors.New("sqlite bundle manifest size limit exceeded")
)

func validateSQLiteBundleManifest(manifest SQLiteBundleManifest, app, archive string) error {
	snapshotScoped := manifest.SnapshotID != ""
	if snapshotScoped && !validSQLiteSnapshotID(manifest.SnapshotID) {
		return errors.New("sqlite bundle snapshot id must be empty or a lowercase sha256 digest")
	}
	if manifest.Format != SQLiteGzipChunkedBundleFormat {
		return fmt.Errorf("sqlite bundle format must be %q", SQLiteGzipChunkedBundleFormat)
	}
	if app == "" || archive == "" || manifest.App != app || manifest.Archive != archive {
		return errors.New("sqlite bundle manifest app and archive must match the upload route")
	}
	if manifest.ContentType != "" && manifest.ContentType != "application/vnd.sqlite3" {
		return errors.New("sqlite bundle content type must be application/vnd.sqlite3 when set")
	}
	if snapshotScoped && manifest.GeneratedAt != "" {
		return errors.New("snapshot sqlite bundle generated_at must be omitted")
	}
	if manifest.GeneratedAt != "" {
		if err := validateSQLiteBundleMetadata(manifest.GeneratedAt, "sqlite bundle generated_at"); err != nil {
			return err
		}
	}
	if manifest.Compression.Algorithm != SQLiteGzipCompression {
		return fmt.Errorf("sqlite bundle compression must be %q", SQLiteGzipCompression)
	}
	if snapshotScoped {
		if manifest.Reconstruct != "" && manifest.Reconstruct != snapshotSQLiteReconstructSteps {
			return fmt.Errorf("sqlite bundle reconstruct must be %q", snapshotSQLiteReconstructSteps)
		}
		for name := range manifest.Privacy {
			if err := validateSQLiteBundleMapKey(name, "sqlite bundle privacy key"); err != nil {
				return err
			}
		}
	}
	if err := validateSQLiteBundleManifestLimits(manifest); err != nil {
		return err
	}
	if manifest.Object.Key != SQLiteSnapshotObjectKey(app, archive, manifest.SnapshotID) {
		return errors.New("sqlite bundle object key must match the upload route")
	}
	if !validSQLiteBundleSHA256(manifest.Object.SHA256, snapshotScoped) {
		return errors.New("sqlite bundle object sha256 must be a valid digest")
	}
	if snapshotScoped && manifest.Object.SHA256 != manifest.SnapshotID {
		return errors.New("sqlite bundle snapshot id must equal the object sha256")
	}
	if manifest.CompressedObject.Key != SQLiteSnapshotCompressedObjectKey(
		app,
		archive,
		manifest.SnapshotID,
		manifest.CompressedObject.SHA256,
	) {
		return errors.New("sqlite bundle compressed object key must match the upload route")
	}
	if !validSQLiteBundleSHA256(manifest.CompressedObject.SHA256, snapshotScoped) {
		return errors.New("sqlite bundle compressed object sha256 must be a valid digest")
	}
	for index, part := range manifest.Parts {
		if !validSQLiteBundleSHA256(part.SHA256, snapshotScoped) {
			return fmt.Errorf("sqlite bundle part %d sha256 must be a valid digest", index)
		}
		if part.Key != SQLiteSnapshotBundlePartKey(
			app,
			archive,
			manifest.SnapshotID,
			part.SHA256,
			index,
		) {
			return fmt.Errorf("sqlite bundle part %d key must match the upload route", index)
		}
	}
	for name, count := range manifest.Counts {
		if name == "" {
			return errors.New("sqlite bundle count names must not be empty")
		}
		if count < 0 {
			return fmt.Errorf("sqlite bundle count %q must not be negative", name)
		}
		if snapshotScoped {
			if err := validateSQLiteBundleMapKey(name, "sqlite bundle count name"); err != nil {
				return err
			}
			if count > maxSQLiteBundleSafeInteger {
				return fmt.Errorf(
					"sqlite bundle count %q must be a non-negative safe integer",
					name,
				)
			}
		}
	}
	return nil
}

func validateSQLiteBundleMapKey(value, label string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	return validateSQLiteBundleMetadata(value, label)
}

func validateSQLiteBundleMetadata(value, label string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if len(value) > maxSQLiteBundleMetadataBytes {
		return fmt.Errorf("%s must not exceed %d UTF-8 bytes", label, maxSQLiteBundleMetadataBytes)
	}
	return nil
}

func validSQLiteBundleSHA256(value string, canonical bool) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if char >= '0' && char <= '9' {
			continue
		}
		if char >= 'a' && char <= 'f' {
			continue
		}
		if !canonical && char >= 'A' && char <= 'F' {
			continue
		}
		return false
	}
	return true
}

func validateSQLiteBundlePartLimit(index int, size int64, snapshotScoped bool) error {
	if index < 0 {
		return fmt.Errorf("sqlite bundle part index must not be negative")
	}
	if snapshotScoped && index >= maxSQLiteBundleParts {
		return fmt.Errorf("sqlite bundle part index must be between 0 and %d", maxSQLiteBundleParts-1)
	}
	if snapshotScoped && size < 0 {
		return fmt.Errorf("sqlite bundle part %d size must be non-negative", index)
	}
	if !snapshotScoped && size < -1 {
		return fmt.Errorf("sqlite bundle part %d size must be -1 or non-negative", index)
	}
	if snapshotScoped && size > DefaultSQLiteBundleChunkSize {
		return fmt.Errorf(
			"sqlite bundle part %d size must be between 0 and %d bytes",
			index,
			DefaultSQLiteBundleChunkSize,
		)
	}
	return nil
}

func validateSQLiteBundleManifestLimits(manifest SQLiteBundleManifest) error {
	snapshotScoped := manifest.SnapshotID != ""
	if len(manifest.Parts) == 0 {
		return fmt.Errorf("sqlite bundle manifest must contain at least one part")
	}
	if snapshotScoped && len(manifest.Parts) > maxSQLiteBundleParts {
		return fmt.Errorf("sqlite bundle manifest must contain between 1 and %d parts", maxSQLiteBundleParts)
	}
	if manifest.Object.Size <= 0 {
		return fmt.Errorf("sqlite bundle object size must be positive")
	}
	if snapshotScoped && manifest.Object.Size > maxSQLiteBundleObjectSize {
		return fmt.Errorf("sqlite bundle object size must be between 1 and %d bytes", maxSQLiteBundleObjectSize)
	}
	if manifest.CompressedObject.Size <= 0 {
		return fmt.Errorf("sqlite bundle compressed size must be positive")
	}
	if snapshotScoped && manifest.CompressedObject.Size > maxSQLiteBundleCompressedSize {
		return fmt.Errorf("sqlite bundle compressed size must be between 1 and %d bytes", maxSQLiteBundleCompressedSize)
	}
	var total int64
	for index, part := range manifest.Parts {
		if part.Index != index {
			return fmt.Errorf("sqlite bundle manifest part %d has index %d", index, part.Index)
		}
		if part.Size <= 0 {
			return fmt.Errorf("sqlite bundle part %d size must be positive", part.Index)
		}
		if err := validateSQLiteBundlePartLimit(part.Index, part.Size, snapshotScoped); err != nil {
			return err
		}
		if snapshotScoped && total > maxSQLiteBundleCompressedSize-part.Size {
			return fmt.Errorf("sqlite bundle parts exceed %d compressed bytes", maxSQLiteBundleCompressedSize)
		}
		if total > manifest.CompressedObject.Size ||
			part.Size > manifest.CompressedObject.Size-total {
			return fmt.Errorf("sqlite bundle part sizes exceed the declared compressed size")
		}
		total += part.Size
	}
	if total != manifest.CompressedObject.Size {
		return fmt.Errorf(
			"sqlite bundle part sizes total %d bytes, want compressed size %d",
			total,
			manifest.CompressedObject.Size,
		)
	}
	return nil
}
