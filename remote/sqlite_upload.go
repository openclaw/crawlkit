package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type SQLiteUploadRequest struct {
	Body          io.Reader
	Size          int64
	ContentSHA256 string
	SchemaName    string
	SchemaVersion int
	SchemaHash    string
	SourceSyncAt  string
}

type SQLiteUploadResult struct {
	App      string        `json:"app,omitempty"`
	Archive  string        `json:"archive,omitempty"`
	Complete bool          `json:"complete,omitempty"`
	Object   *SQLiteObject `json:"object,omitempty"`
}

type SQLiteObject struct {
	Key         string `json:"key,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ETag        string `json:"etag,omitempty"`
	UploadedAt  string `json:"uploaded_at,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

type SQLiteBundleUploadResult struct {
	App      string        `json:"app,omitempty"`
	Archive  string        `json:"archive,omitempty"`
	Complete bool          `json:"complete,omitempty"`
	Bundle   *SQLiteBundle `json:"bundle,omitempty"`
}

type SQLiteBundle struct {
	Key         string                `json:"key,omitempty"`
	Size        int64                 `json:"size,omitempty"`
	ETag        string                `json:"etag,omitempty"`
	UploadedAt  string                `json:"uploaded_at,omitempty"`
	ContentType string                `json:"content_type,omitempty"`
	Manifest    *SQLiteBundleManifest `json:"manifest,omitempty"`
}

func (c *Client) UploadSQLite(ctx context.Context, app, archive string, upload SQLiteUploadRequest) (SQLiteUploadResult, error) {
	if upload.Body == nil {
		return SQLiteUploadResult{}, errors.New("sqlite upload body is required")
	}
	headers := http.Header{}
	headers.Set("content-type", "application/vnd.sqlite3")
	setHeader(headers, "x-crawl-schema-name", upload.SchemaName)
	setHeader(headers, "x-crawl-schema-version", intHeader(upload.SchemaVersion))
	setHeader(headers, "x-crawl-schema-hash", upload.SchemaHash)
	setHeader(headers, "x-crawl-source-sync-at", upload.SourceSyncAt)
	setHeader(headers, "x-crawl-content-sha256", upload.ContentSHA256)
	var out SQLiteUploadResult
	err := c.doRaw(ctx, http.MethodPut, archivePath(app, archive, "sqlite"), upload.Body, upload.Size, headers, &out, true)
	return out, err
}

func (c *Client) UploadSQLiteBundlePart(ctx context.Context, app, archive string, part SQLiteBundlePartUpload) (SQLiteUploadResult, error) {
	if part.Body == nil {
		return SQLiteUploadResult{}, errors.New("sqlite bundle part body is required")
	}
	if part.SnapshotID != "" && !validSQLiteSnapshotID(part.SnapshotID) {
		return SQLiteUploadResult{}, errors.New("sqlite bundle snapshot id must be empty or a lowercase sha256 digest")
	}
	snapshotScoped := part.SnapshotID != ""
	if err := validateSQLiteBundlePartLimit(part.Index, part.Size, snapshotScoped); err != nil {
		return SQLiteUploadResult{}, err
	}
	body := part.Body
	if snapshotScoped {
		bounded, err := sqliteBundleDeclaredSizeReader(body, part.Size)
		if err != nil {
			return SQLiteUploadResult{}, err
		}
		body = bounded
	}
	headers := http.Header{}
	headers.Set("content-type", "application/gzip")
	headers.Set("x-crawl-sqlite-upload", "bundle-part")
	headers.Set("x-crawl-bundle-part-index", fmt.Sprintf("%d", part.Index))
	setHeader(headers, "x-crawl-content-sha256", part.SHA256)
	setHeader(headers, "x-crawl-compression", part.Compression)
	setHeader(headers, "x-crawl-snapshot-id", part.SnapshotID)
	var out SQLiteUploadResult
	err := c.doRaw(ctx, http.MethodPut, archivePath(app, archive, "sqlite"), body, part.Size, headers, &out, true)
	return out, err
}

func (c *Client) UploadSQLiteBundleFiles(ctx context.Context, app, archive string, manifest SQLiteBundleManifest, parts []SQLiteBundlePartFile) (SQLiteBundleUploadResult, error) {
	preparedManifest, manifestBody, err := prepareSQLiteBundleManifest(app, archive, manifest)
	if err != nil {
		return SQLiteBundleUploadResult{}, err
	}
	if preparedManifest.SnapshotID == "" {
		expectedParts, err := validateMutableSQLiteBundleFiles(ctx, preparedManifest, parts)
		if err != nil {
			return SQLiteBundleUploadResult{}, err
		}
		return c.uploadMutableSQLiteBundleFiles(
			ctx,
			app,
			archive,
			preparedManifest,
			manifestBody,
			parts,
			expectedParts,
		)
	}
	validatedParts, err := openValidatedSnapshotSQLiteBundleFiles(ctx, preparedManifest, parts)
	if err != nil {
		return SQLiteBundleUploadResult{}, err
	}
	defer closeValidatedSnapshotSQLiteBundleFiles(validatedParts)
	for _, part := range validatedParts {
		_, uploadErr := c.UploadSQLiteBundlePart(ctx, app, archive, SQLiteBundlePartUpload{
			Index:       part.part.Index,
			Body:        part.file,
			Size:        part.part.Size,
			SHA256:      part.part.SHA256,
			Compression: SQLiteGzipCompression,
			SnapshotID:  preparedManifest.SnapshotID,
		})
		if uploadErr != nil {
			return SQLiteBundleUploadResult{}, uploadErr
		}
	}
	return c.uploadSQLiteBundleManifest(
		ctx,
		app,
		archive,
		preparedManifest.SnapshotID,
		manifestBody,
	)
}

func (c *Client) uploadMutableSQLiteBundleFiles(
	ctx context.Context,
	app, archive string,
	manifest SQLiteBundleManifest,
	manifestBody []byte,
	parts []SQLiteBundlePartFile,
	expectedParts map[int]validatedMutableSQLiteBundlePartFile,
) (SQLiteBundleUploadResult, error) {
	for _, part := range parts {
		validated := expectedParts[part.Index]
		expected := validated.part
		file, err := os.Open(part.Path)
		if err != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf("open sqlite bundle part %d: %w", part.Index, err)
		}
		infoBefore, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return SQLiteBundleUploadResult{}, fmt.Errorf("stat sqlite bundle part %d: %w", part.Index, err)
		}
		if !infoBefore.Mode().IsRegular() || infoBefore.Size() != validated.localSize {
			_ = file.Close()
			return SQLiteBundleUploadResult{}, fmt.Errorf(
				"sqlite bundle part file %d must be a %d-byte regular file",
				part.Index,
				validated.localSize,
			)
		}
		bounded, err := sqliteBundleDeclaredSizeReader(file, validated.localSize)
		if err != nil {
			_ = file.Close()
			return SQLiteBundleUploadResult{}, err
		}
		uploadBody := &io.LimitedReader{
			R: bounded,
			N: validated.localSize,
		}
		hash := sha256.New()
		var streamed byteCounter
		body := io.TeeReader(uploadBody, io.MultiWriter(hash, &streamed))
		_, uploadErr := c.UploadSQLiteBundlePart(ctx, app, archive, SQLiteBundlePartUpload{
			Index:       expected.Index,
			Body:        body,
			Size:        expected.Size,
			SHA256:      expected.SHA256,
			Compression: SQLiteGzipCompression,
		})
		var drainErr error
		var growthProbeSize int64
		if uploadErr == nil {
			_, drainErr = copyWithContext(ctx, io.Discard, body)
			if drainErr == nil {
				growthProbeSize, drainErr = copyWithContext(ctx, io.Discard, bounded)
			}
		}
		infoAfter, statErr := file.Stat()
		closeErr := file.Close()
		if uploadErr != nil {
			return SQLiteBundleUploadResult{}, uploadErr
		}
		if drainErr != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf(
				"verify sqlite bundle part %d upload: %w",
				part.Index,
				drainErr,
			)
		}
		if statErr != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf("restat sqlite bundle part %d: %w", part.Index, statErr)
		}
		if closeErr != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf("close sqlite bundle part %d: %w", part.Index, closeErr)
		}
		expectedSHA256 := strings.TrimSpace(expected.SHA256)
		digestChanged := expectedSHA256 != "" &&
			!strings.EqualFold(fmt.Sprintf("%x", hash.Sum(nil)), expectedSHA256)
		if !os.SameFile(infoBefore, infoAfter) ||
			infoBefore.Size() != validated.localSize ||
			infoAfter.Size() != validated.localSize ||
			int64(streamed) != validated.localSize ||
			growthProbeSize != 0 ||
			digestChanged {
			return SQLiteBundleUploadResult{}, fmt.Errorf(
				"sqlite bundle part file %d changed during upload",
				part.Index,
			)
		}
	}
	return c.uploadSQLiteBundleManifest(ctx, app, archive, manifest.SnapshotID, manifestBody)
}

type validatedMutableSQLiteBundlePartFile struct {
	part      SQLiteBundlePart
	localSize int64
}

type byteCounter int64

func (counter *byteCounter) Write(p []byte) (int, error) {
	*counter += byteCounter(len(p))
	return len(p), nil
}

func (c *Client) UploadSQLiteBundleManifest(ctx context.Context, app, archive string, manifest SQLiteBundleManifest) (SQLiteBundleUploadResult, error) {
	_, manifestBody, err := prepareSQLiteBundleManifest(app, archive, manifest)
	if err != nil {
		return SQLiteBundleUploadResult{}, err
	}
	return c.uploadSQLiteBundleManifest(ctx, app, archive, manifest.SnapshotID, manifestBody)
}

func (c *Client) uploadSQLiteBundleManifest(
	ctx context.Context,
	app, archive, snapshotID string,
	manifestBody []byte,
) (SQLiteBundleUploadResult, error) {
	headers := http.Header{}
	headers.Set("content-type", "application/json")
	headers.Set("x-crawl-sqlite-upload", "bundle-manifest")
	setHeader(headers, "x-crawl-snapshot-id", snapshotID)
	var out SQLiteBundleUploadResult
	err := c.doRaw(
		ctx,
		http.MethodPut,
		archivePath(app, archive, "sqlite"),
		bytes.NewReader(manifestBody),
		int64(len(manifestBody)),
		headers,
		&out,
		true,
	)
	return out, err
}

func setHeader(headers http.Header, name, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		headers.Set(name, value)
	}
}

func intHeader(value int) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", value)
}
