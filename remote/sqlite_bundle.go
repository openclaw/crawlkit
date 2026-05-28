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
}

func BuildGzipSQLiteBundle(ctx context.Context, opts SQLiteBundleBuildOptions) (SQLiteBundleBuild, error) {
	if opts.SourcePath == "" {
		return SQLiteBundleBuild{}, fmt.Errorf("sqlite bundle source path is required")
	}
	sourceInfo, err := os.Stat(opts.SourcePath)
	if err != nil {
		return SQLiteBundleBuild{}, fmt.Errorf("stat sqlite bundle source: %w", err)
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
	if err := gzipFile(ctx, opts.SourcePath, compressedPath, level); err != nil {
		cleanup()
		return SQLiteBundleBuild{}, err
	}
	sourceSHA, err := fileSHA256(ctx, opts.SourcePath)
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
	parts, err := splitBundleParts(ctx, compressedPath, tmpDir, opts.App, opts.Archive, chunkSize)
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
		GeneratedAt: generatedAt.Format(time.RFC3339Nano),
		ContentType: contentType,
		Compression: SQLiteBundleCompression{
			Algorithm: SQLiteGzipCompression,
		},
		Privacy: opts.Privacy,
		Object: SQLiteBundleObject{
			Key:    SQLiteObjectKey(opts.App, opts.Archive),
			Size:   sourceInfo.Size(),
			SHA256: sourceSHA,
		},
		CompressedObject: SQLiteBundleObject{
			Key:    SQLiteCompressedObjectKey(opts.App, opts.Archive),
			Size:   compressedInfo.Size(),
			SHA256: compressedSHA,
		},
		Reconstruct: "concatenate parts in index order to current.db.gz, then gzip-decompress to current.db",
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

func gzipFile(ctx context.Context, sourcePath, targetPath string, level int) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open sqlite bundle source: %w", err)
	}
	defer func() { _ = source.Close() }()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create compressed sqlite bundle: %w", err)
	}
	defer func() { _ = target.Close() }()
	gzw, err := gzip.NewWriterLevel(target, level)
	if err != nil {
		return fmt.Errorf("create gzip writer: %w", err)
	}
	if err := copyWithContext(ctx, gzw, source); err != nil {
		_ = gzw.Close()
		return fmt.Errorf("compress sqlite bundle: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return fmt.Errorf("finish compressed sqlite bundle: %w", err)
	}
	return nil
}

func splitBundleParts(ctx context.Context, sourcePath, dir, app, archive string, chunkSize int64) ([]SQLiteBundlePartFile, error) {
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
		parts = append(parts, SQLiteBundlePartFile{
			SQLiteBundlePart: SQLiteBundlePart{
				Index:  index,
				Key:    SQLiteBundlePartKey(app, archive, index),
				Size:   written,
				SHA256: fmt.Sprintf("%x", hash.Sum(nil)),
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
	if err := copyWithContext(ctx, hash, file); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	buf := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
