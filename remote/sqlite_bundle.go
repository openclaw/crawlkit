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
	SQLiteGzipChunkedBundleFormat       = "sqlite-gzip-chunked-v1"
	SQLiteGzipCompression               = "gzip"
	DefaultSQLiteBundleChunkSize        = int64(256 * 1024 * 1024)
	DefaultMutableSQLiteBundleChunkSize = int64(64 * 1024 * 1024)

	maxSQLiteBundleManifestBytes  = int64(64 * 1024)
	maxSQLiteBundleCompressedSize = int64(512 * 1024 * 1024)
	maxSQLiteBundleObjectSize     = int64(4 * 1024 * 1024 * 1024)
	maxSQLiteBundleParts          = 8
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

type sqliteBundleBuildLimits struct {
	maxCompressedSize int64
	maxParts          int
}

type sqliteBundleSourceCopy func(context.Context, io.Writer, io.Reader) (int64, error)

func BuildGzipSQLiteBundle(ctx context.Context, opts SQLiteBundleBuildOptions) (SQLiteBundleBuild, error) {
	return buildGzipSQLiteBundle(ctx, opts, false)
}

// BuildSnapshotGzipSQLiteBundle builds an immutable, content-addressed bundle.
// Its manifest omits GeneratedAt so identical source bytes and options produce
// identical JSON. Callers must treat a different representation for the same
// source snapshot as a conflict at the immutable source manifest key.
func BuildSnapshotGzipSQLiteBundle(ctx context.Context, opts SQLiteBundleBuildOptions) (SQLiteBundleBuild, error) {
	return buildGzipSQLiteBundle(ctx, opts, true)
}

func buildGzipSQLiteBundle(ctx context.Context, opts SQLiteBundleBuildOptions, snapshotScoped bool) (SQLiteBundleBuild, error) {
	return buildGzipSQLiteBundleWithLimits(ctx, opts, snapshotScoped, sqliteBundleBuildLimits{
		maxCompressedSize: maxSQLiteBundleCompressedSize,
		maxParts:          maxSQLiteBundleParts,
	})
}

func buildGzipSQLiteBundleWithLimits(
	ctx context.Context,
	opts SQLiteBundleBuildOptions,
	snapshotScoped bool,
	limits sqliteBundleBuildLimits,
) (SQLiteBundleBuild, error) {
	return buildGzipSQLiteBundleWithSourceCopy(
		ctx,
		opts,
		snapshotScoped,
		limits,
		copyWithContext,
	)
}

func buildGzipSQLiteBundleWithSourceCopy(
	ctx context.Context,
	opts SQLiteBundleBuildOptions,
	snapshotScoped bool,
	limits sqliteBundleBuildLimits,
	copySource sqliteBundleSourceCopy,
) (SQLiteBundleBuild, error) {
	if opts.SourcePath == "" {
		return SQLiteBundleBuild{}, fmt.Errorf("sqlite bundle source path is required")
	}
	if limits.maxCompressedSize <= 0 || limits.maxParts <= 0 {
		return SQLiteBundleBuild{}, fmt.Errorf("sqlite bundle build limits must be positive")
	}
	if copySource == nil {
		return SQLiteBundleBuild{}, fmt.Errorf("sqlite bundle source copier is required")
	}
	chunkSize := sqliteBundleChunkSize(opts.ChunkSize, snapshotScoped)
	if chunkSize > DefaultSQLiteBundleChunkSize {
		return SQLiteBundleBuild{}, fmt.Errorf(
			"sqlite bundle chunk size must not exceed %d bytes",
			DefaultSQLiteBundleChunkSize,
		)
	}
	source, err := os.Open(opts.SourcePath)
	if err != nil {
		return SQLiteBundleBuild{}, fmt.Errorf("open sqlite bundle source: %w", err)
	}
	defer func() { _ = source.Close() }()
	sourceInfo, err := source.Stat()
	if err != nil {
		return SQLiteBundleBuild{}, fmt.Errorf("stat sqlite bundle source: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return SQLiteBundleBuild{}, fmt.Errorf("sqlite bundle source must be a regular file")
	}
	pathInfo, err := os.Stat(opts.SourcePath)
	if err != nil {
		return SQLiteBundleBuild{}, fmt.Errorf("stat sqlite bundle source path: %w", err)
	}
	if !os.SameFile(sourceInfo, pathInfo) {
		return SQLiteBundleBuild{}, fmt.Errorf("sqlite bundle source changed before compression")
	}
	if err := validateSQLiteBundleSourceSize(sourceInfo.Size()); err != nil {
		return SQLiteBundleBuild{}, err
	}
	level := opts.CompressionLevel
	if level == 0 {
		level = gzip.DefaultCompression
	}
	generatedAt := ""
	if !snapshotScoped {
		value := opts.GeneratedAt
		if value.IsZero() {
			value = time.Now().UTC()
		}
		generatedAt = value.Format(time.RFC3339Nano)
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
	compressedLimit := limits.maxCompressedSize
	compressedLimitErr := fmt.Errorf(
		"compressed sqlite bundle exceeds %d bytes",
		limits.maxCompressedSize,
	)
	partCapacity := chunkSize * int64(limits.maxParts)
	if partCapacity < compressedLimit {
		compressedLimit = partCapacity
		compressedLimitErr = fmt.Errorf(
			"compressed sqlite bundle requires more than %d parts at %d bytes each",
			limits.maxParts,
			chunkSize,
		)
	}
	sourceSHA, sourceSize, err := gzipFile(
		ctx,
		source,
		compressedPath,
		level,
		compressedLimit,
		compressedLimitErr,
		copySource,
	)
	if err != nil {
		cleanup()
		return SQLiteBundleBuild{}, err
	}
	if err := validateSQLiteBundleSourceStable(
		ctx,
		source,
		opts.SourcePath,
		sourceInfo,
		sourceSize,
		sourceSHA,
	); err != nil {
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
	parts, err := splitBundleParts(
		ctx,
		compressedPath,
		tmpDir,
		opts.App,
		opts.Archive,
		snapshotID,
		chunkSize,
		limits,
	)
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
		GeneratedAt: generatedAt,
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

func validateSQLiteBundleSourceSize(size int64) error {
	if size <= 0 || size > maxSQLiteBundleObjectSize {
		return fmt.Errorf(
			"sqlite bundle object size must be between 1 and %d bytes",
			maxSQLiteBundleObjectSize,
		)
	}
	return nil
}

func sqliteBundleChunkSize(requested int64, snapshotScoped bool) int64 {
	if requested > 0 {
		return requested
	}
	if snapshotScoped {
		return DefaultSQLiteBundleChunkSize
	}
	return DefaultMutableSQLiteBundleChunkSize
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

type sqliteBundleBoundedWriter struct {
	writer   io.Writer
	limit    int64
	written  int64
	limitErr error
}

func (w *sqliteBundleBoundedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.written
	if remaining <= 0 {
		return 0, w.limitErr
	}
	if int64(len(p)) > remaining {
		n, err := w.writer.Write(p[:remaining])
		w.written += int64(n)
		if err != nil {
			return n, err
		}
		if int64(n) != remaining {
			return n, io.ErrShortWrite
		}
		return n, w.limitErr
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func gzipFile(
	ctx context.Context,
	source io.Reader,
	targetPath string,
	level int,
	maxCompressedSize int64,
	limitErr error,
	copySource sqliteBundleSourceCopy,
) (string, int64, error) {
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create compressed sqlite bundle: %w", err)
	}
	defer func() { _ = target.Close() }()
	boundedTarget := &sqliteBundleBoundedWriter{
		writer:   target,
		limit:    maxCompressedSize,
		limitErr: limitErr,
	}
	gzw, err := gzip.NewWriterLevel(boundedTarget, level)
	if err != nil {
		return "", 0, fmt.Errorf("create gzip writer: %w", err)
	}
	hash := sha256.New()
	sourceSize, err := copySource(ctx, io.MultiWriter(gzw, hash), source)
	if err != nil {
		_ = gzw.Close()
		return "", 0, fmt.Errorf("compress sqlite bundle: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return "", 0, fmt.Errorf("finish compressed sqlite bundle: %w", err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), sourceSize, nil
}

func validateSQLiteBundleSourceStable(
	ctx context.Context,
	source *os.File,
	sourcePath string,
	initialInfo os.FileInfo,
	sourceSize int64,
	sourceSHA256 string,
) error {
	if err := validateSQLiteBundleSourceState(source, sourcePath, initialInfo, sourceSize); err != nil {
		return err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind sqlite bundle source: %w", err)
	}
	hash := sha256.New()
	verifiedSize, err := copyWithContext(ctx, hash, source)
	if err != nil {
		return fmt.Errorf("verify sqlite bundle source: %w", err)
	}
	if verifiedSize != sourceSize ||
		fmt.Sprintf("%x", hash.Sum(nil)) != sourceSHA256 {
		return fmt.Errorf("sqlite bundle source changed during bundle construction")
	}
	return validateSQLiteBundleSourceState(
		source,
		sourcePath,
		initialInfo,
		verifiedSize,
	)
}

func validateSQLiteBundleSourceState(
	source *os.File,
	sourcePath string,
	initialInfo os.FileInfo,
	readSize int64,
) error {
	currentInfo, err := source.Stat()
	if err != nil {
		return fmt.Errorf("restat sqlite bundle source: %w", err)
	}
	pathInfo, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("restat sqlite bundle source path: %w", err)
	}
	if !os.SameFile(initialInfo, currentInfo) ||
		!os.SameFile(initialInfo, pathInfo) ||
		!currentInfo.Mode().IsRegular() ||
		initialInfo.Size() != currentInfo.Size() ||
		initialInfo.Size() != pathInfo.Size() ||
		initialInfo.Size() != readSize ||
		!initialInfo.ModTime().Equal(currentInfo.ModTime()) ||
		!initialInfo.ModTime().Equal(pathInfo.ModTime()) {
		return fmt.Errorf("sqlite bundle source changed during bundle construction")
	}
	return nil
}

func splitBundleParts(
	ctx context.Context,
	sourcePath,
	dir,
	app,
	archive,
	snapshotID string,
	chunkSize int64,
	limits sqliteBundleBuildLimits,
) ([]SQLiteBundlePartFile, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open compressed sqlite bundle: %w", err)
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat compressed sqlite bundle: %w", err)
	}
	if info.Size() <= 0 {
		return nil, fmt.Errorf("compressed sqlite bundle is empty")
	}
	if info.Size() > limits.maxCompressedSize {
		return nil, fmt.Errorf(
			"compressed sqlite bundle exceeds %d bytes",
			limits.maxCompressedSize,
		)
	}
	partCount := int((info.Size()-1)/chunkSize) + 1
	if partCount > limits.maxParts {
		return nil, fmt.Errorf(
			"compressed sqlite bundle requires %d parts, maximum is %d",
			partCount,
			limits.maxParts,
		)
	}
	parts := make([]SQLiteBundlePartFile, 0, partCount)
	for index := 0; index < partCount; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		partPath := filepath.Join(dir, fmt.Sprintf("current.db.gz.part-%04d", index))
		partFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create sqlite bundle part: %w", err)
		}
		hash := sha256.New()
		partSize := min(chunkSize, info.Size()-int64(index)*chunkSize)
		written, copyErr := io.CopyN(io.MultiWriter(partFile, hash), source, partSize)
		closeErr := partFile.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("write sqlite bundle part: %w", copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close sqlite bundle part: %w", closeErr)
		}
		if written != partSize {
			return nil, fmt.Errorf(
				"write sqlite bundle part %d: wrote %d bytes, want %d",
				index,
				written,
				partSize,
			)
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
