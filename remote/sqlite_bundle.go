package remote

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const (
	SQLiteGzipChunkedBundleFormat = "sqlite-gzip-chunked-v1"
	SQLiteGzipCompression         = "gzip"
	DefaultSQLiteBundleChunkSize  = int64(256 * 1024 * 1024)
)

type SQLiteBundleObject struct {
	Key    string `json:"key,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type SQLiteBundleCompression struct {
	Algorithm string `json:"algorithm,omitempty"`
}

type SQLiteBundlePart struct {
	Index  int    `json:"index"`
	Key    string `json:"key,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type SQLiteBundleManifest struct {
	Format           string                  `json:"format"`
	App              string                  `json:"app"`
	Archive          string                  `json:"archive"`
	SnapshotID       string                  `json:"snapshot_id,omitempty"`
	GeneratedAt      string                  `json:"generated_at,omitempty"`
	ContentType      string                  `json:"content_type,omitempty"`
	Compression      SQLiteBundleCompression `json:"compression,omitempty"`
	Privacy          map[string]any          `json:"privacy,omitempty"`
	Object           SQLiteBundleObject      `json:"object"`
	CompressedObject SQLiteBundleObject      `json:"compressed_object"`
	Reconstruct      string                  `json:"reconstruct,omitempty"`
	Counts           map[string]int64        `json:"counts,omitempty"`
	Parts            []SQLiteBundlePart      `json:"parts"`
}

type SQLiteBundlePartFile struct {
	SQLiteBundlePart
	Path string
}

type SQLiteBundleBuild struct {
	Manifest       SQLiteBundleManifest
	CompressedPath string
	Parts          []SQLiteBundlePartFile
	Cleanup        func()
}

type SQLiteBundleBuildOptions struct {
	App              string
	Archive          string
	SourcePath       string
	WorkDir          string
	ChunkSize        int64
	CompressionLevel int
	GeneratedAt      time.Time
	ContentType      string
	Privacy          map[string]any
	Counts           map[string]int64
}

type SQLiteBundlePartUpload struct {
	Index       int
	Body        io.Reader
	Size        int64
	SHA256      string
	Compression string
	SnapshotID  string
}

func BuildGzipSQLiteBundle(ctx context.Context, opts SQLiteBundleBuildOptions) (SQLiteBundleBuild, error) {
	return buildGzipSQLiteBundle(ctx, opts, false)
}

func BuildSnapshotGzipSQLiteBundle(ctx context.Context, opts SQLiteBundleBuildOptions) (SQLiteBundleBuild, error) {
	return buildGzipSQLiteBundle(ctx, opts, true)
}

func buildGzipSQLiteBundle(ctx context.Context, opts SQLiteBundleBuildOptions, snapshotScoped bool) (SQLiteBundleBuild, error) {
	if opts.SourcePath == "" {
		return SQLiteBundleBuild{}, fmt.Errorf("sqlite bundle source path is required")
	}
	chunkSize := opts.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultSQLiteBundleChunkSize
	}
	level := opts.CompressionLevel
	if level == 0 {
		level = gzip.DefaultCompression
	}
	generatedAt := opts.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/vnd.sqlite3"
	}
	tmpDir, err := os.MkdirTemp(opts.WorkDir, "crawl-sqlite-bundle-*")
	if err != nil {
		return SQLiteBundleBuild{}, fmt.Errorf("create sqlite bundle dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	compressedPath := filepath.Join(tmpDir, "current.db.gz")
	sourceSHA, sourceSize, err := gzipFile(ctx, opts.SourcePath, compressedPath, level)
	if err != nil {
		cleanup()
		return SQLiteBundleBuild{}, err
	}
	compressedInfo, err := os.Stat(compressedPath)
	if err != nil {
		cleanup()
		return SQLiteBundleBuild{}, fmt.Errorf("stat compressed sqlite bundle: %w", err)
	}
	compressedSHA, err := fileSHA256(ctx, compressedPath)
	if err != nil {
		cleanup()
		return SQLiteBundleBuild{}, err
	}
	snapshotID := ""
	objectKey := SQLiteObjectKey(opts.App, opts.Archive)
	compressedObjectKey := SQLiteCompressedObjectKey(opts.App, opts.Archive)
	reconstruct := "concatenate parts in index order to current.db.gz, then gzip-decompress to current.db"
	if snapshotScoped {
		snapshotID = sourceSHA
		objectKey = SQLiteSnapshotObjectKey(opts.App, opts.Archive, snapshotID)
		compressedObjectKey = SQLiteSnapshotCompressedObjectKey(opts.App, opts.Archive, snapshotID, compressedSHA)
		reconstruct = "concatenate parts in index order to archive.db.gz, then gzip-decompress to archive.db"
	}
	parts, err := splitBundleParts(ctx, compressedPath, tmpDir, opts.App, opts.Archive, snapshotID, chunkSize)
	if err != nil {
		cleanup()
		return SQLiteBundleBuild{}, err
	}
	manifestParts := make([]SQLiteBundlePart, len(parts))
	for i, part := range parts {
		manifestParts[i] = part.SQLiteBundlePart
	}
	manifest := SQLiteBundleManifest{
		Format:      SQLiteGzipChunkedBundleFormat,
		App:         opts.App,
		Archive:     opts.Archive,
		SnapshotID:  snapshotID,
		GeneratedAt: generatedAt.Format(time.RFC3339Nano),
		ContentType: contentType,
		Compression: SQLiteBundleCompression{
			Algorithm: SQLiteGzipCompression,
		},
		Privacy: opts.Privacy,
		Object: SQLiteBundleObject{
			Key:    objectKey,
			Size:   sourceSize,
			SHA256: sourceSHA,
		},
		CompressedObject: SQLiteBundleObject{
			Key:    compressedObjectKey,
			Size:   compressedInfo.Size(),
			SHA256: compressedSHA,
		},
		Reconstruct: reconstruct,
		Counts:      opts.Counts,
		Parts:       manifestParts,
	}
	return SQLiteBundleBuild{
		Manifest:       manifest,
		CompressedPath: compressedPath,
		Parts:          parts,
		Cleanup:        cleanup,
	}, nil
}

func SQLiteObjectKey(app, archive string) string {
	return fmt.Sprintf("v1/%s/%s/sqlite/current.db", url.PathEscape(app), url.PathEscape(archive))
}

func SQLiteCompressedObjectKey(app, archive string) string {
	return fmt.Sprintf("v1/%s/%s/sqlite/current.db.gz", url.PathEscape(app), url.PathEscape(archive))
}

func SQLiteBundleManifestKey(app, archive string) string {
	return fmt.Sprintf("v1/%s/%s/sqlite/current.manifest.json", url.PathEscape(app), url.PathEscape(archive))
}

func SQLiteBundlePartKey(app, archive string, index int) string {
	return fmt.Sprintf("v1/%s/%s/sqlite/chunks/current.db.gz.part-%04d", url.PathEscape(app), url.PathEscape(archive), index)
}

func SQLiteSnapshotObjectKey(app, archive, snapshotID string) string {
	if snapshotID == "" {
		return SQLiteObjectKey(app, archive)
	}
	if !validSQLiteSnapshotID(snapshotID) {
		return ""
	}
	return fmt.Sprintf(
		"v1/%s/%s/sqlite/snapshots/%s/archive.db",
		url.PathEscape(app),
		url.PathEscape(archive),
		url.PathEscape(snapshotID),
	)
}

func SQLiteSnapshotCompressedObjectKey(app, archive, snapshotID, compressedSHA string) string {
	if snapshotID == "" {
		return SQLiteCompressedObjectKey(app, archive)
	}
	if !validSQLiteSnapshotID(snapshotID) || !validSQLiteSnapshotID(compressedSHA) {
		return ""
	}
	return fmt.Sprintf(
		"v1/%s/%s/sqlite/snapshots/%s/objects/%s/archive.db.gz",
		url.PathEscape(app),
		url.PathEscape(archive),
		url.PathEscape(snapshotID),
		url.PathEscape(compressedSHA),
	)
}

func SQLiteSnapshotBundleManifestKey(app, archive, snapshotID string) string {
	if snapshotID == "" {
		return SQLiteBundleManifestKey(app, archive)
	}
	if !validSQLiteSnapshotID(snapshotID) {
		return ""
	}
	return fmt.Sprintf(
		"v1/%s/%s/sqlite/snapshots/%s/manifest.json",
		url.PathEscape(app),
		url.PathEscape(archive),
		url.PathEscape(snapshotID),
	)
}

func SQLiteSnapshotBundlePartKey(app, archive, snapshotID, partSHA string, index int) string {
	if snapshotID == "" {
		return SQLiteBundlePartKey(app, archive, index)
	}
	if !validSQLiteSnapshotID(snapshotID) || !validSQLiteSnapshotID(partSHA) {
		return ""
	}
	return fmt.Sprintf(
		"v1/%s/%s/sqlite/snapshots/%s/chunks/%s/archive.db.gz.part-%04d",
		url.PathEscape(app),
		url.PathEscape(archive),
		url.PathEscape(snapshotID),
		url.PathEscape(partSHA),
		index,
	)
}

func validSQLiteSnapshotID(snapshotID string) bool {
	if len(snapshotID) != sha256.Size*2 {
		return false
	}
	for _, char := range snapshotID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func gzipFile(ctx context.Context, sourcePath, targetPath string, level int) (string, int64, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", 0, fmt.Errorf("open sqlite bundle source: %w", err)
	}
	defer func() { _ = source.Close() }()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create compressed sqlite bundle: %w", err)
	}
	defer func() { _ = target.Close() }()
	gzw, err := gzip.NewWriterLevel(target, level)
	if err != nil {
		return "", 0, fmt.Errorf("create gzip writer: %w", err)
	}
	hash := sha256.New()
	sourceSize, err := copyWithContext(ctx, io.MultiWriter(gzw, hash), source)
	if err != nil {
		_ = gzw.Close()
		return "", 0, fmt.Errorf("compress sqlite bundle: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return "", 0, fmt.Errorf("finish compressed sqlite bundle: %w", err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), sourceSize, nil
}

func splitBundleParts(ctx context.Context, sourcePath, dir, app, archive, snapshotID string, chunkSize int64) ([]SQLiteBundlePartFile, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open compressed sqlite bundle: %w", err)
	}
	defer func() { _ = source.Close() }()
	var parts []SQLiteBundlePartFile
	for index := 0; ; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		partPath := filepath.Join(dir, fmt.Sprintf("current.db.gz.part-%04d", index))
		partFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create sqlite bundle part: %w", err)
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(partFile, hash), source, chunkSize)
		closeErr := partFile.Close()
		if copyErr != nil && copyErr != io.EOF {
			return nil, fmt.Errorf("write sqlite bundle part: %w", copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close sqlite bundle part: %w", closeErr)
		}
		if written == 0 && copyErr == io.EOF {
			_ = os.Remove(partPath)
			break
		}
		partSHA := fmt.Sprintf("%x", hash.Sum(nil))
		parts = append(parts, SQLiteBundlePartFile{
			SQLiteBundlePart: SQLiteBundlePart{
				Index:  index,
				Key:    SQLiteSnapshotBundlePartKey(app, archive, snapshotID, partSHA, index),
				Size:   written,
				SHA256: partSHA,
			},
			Path: partPath,
		})
		if copyErr == io.EOF {
			break
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("compressed sqlite bundle is empty")
	}
	return parts, nil
}

func fileSHA256(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for sha256: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := copyWithContext(ctx, hash, file); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 1024*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			count, err := dst.Write(buf[:n])
			written += int64(count)
			if err != nil {
				return written, err
			}
			if count != n {
				return written, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
