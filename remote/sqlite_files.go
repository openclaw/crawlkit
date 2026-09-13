package remote

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type validatedSnapshotSQLiteBundlePartFile struct {
	part    SQLiteBundlePart
	file    *os.File
	tempDir string
}

func openValidatedSnapshotSQLiteBundleFiles(
	ctx context.Context,
	manifest SQLiteBundleManifest,
	parts []SQLiteBundlePartFile,
) (_ []validatedSnapshotSQLiteBundlePartFile, err error) {
	if err := validateSQLiteBundleManifest(manifest, manifest.App, manifest.Archive); err != nil {
		return nil, err
	}
	if manifest.SnapshotID == "" {
		return nil, fmt.Errorf("sqlite bundle snapshot id is required for immutable upload staging")
	}
	if len(parts) != len(manifest.Parts) {
		return nil, fmt.Errorf("sqlite bundle has %d part files, want %d", len(parts), len(manifest.Parts))
	}
	tempDir, err := os.MkdirTemp("", "crawl-sqlite-upload-*")
	if err != nil {
		return nil, fmt.Errorf("create sqlite bundle upload snapshot: %w", err)
	}
	validated := make([]validatedSnapshotSQLiteBundlePartFile, 0, len(parts))
	defer func() {
		if err != nil {
			closeValidatedSnapshotSQLiteBundleFiles(validated)
			_ = os.RemoveAll(tempDir)
		}
	}()
	for index, part := range parts {
		expected := manifest.Parts[index]
		if part.SQLiteBundlePart != expected {
			return nil, fmt.Errorf("sqlite bundle part file %d does not match the manifest", index)
		}
		snapshot, err := snapshotSQLiteBundlePart(ctx, tempDir, index, part)
		if err != nil {
			return nil, err
		}
		validated = append(validated, validatedSnapshotSQLiteBundlePartFile{
			part:    expected,
			file:    snapshot,
			tempDir: tempDir,
		})
	}
	if err := validateSnapshotSQLiteBundleContent(ctx, manifest, validated); err != nil {
		return nil, err
	}
	return validated, nil
}

func validateMutableSQLiteBundleFiles(
	ctx context.Context,
	manifest SQLiteBundleManifest,
	parts []SQLiteBundlePartFile,
) (map[int]validatedMutableSQLiteBundlePartFile, error) {
	if len(manifest.Parts) > 0 && len(parts) != len(manifest.Parts) {
		return nil, fmt.Errorf(
			"sqlite bundle has %d part files, want %d",
			len(parts),
			len(manifest.Parts),
		)
	}
	manifestParts := make(map[int]SQLiteBundlePart, len(manifest.Parts))
	for _, part := range manifest.Parts {
		if _, duplicate := manifestParts[part.Index]; duplicate {
			return nil, fmt.Errorf("sqlite bundle manifest repeats part index %d", part.Index)
		}
		manifestParts[part.Index] = part
	}
	expectedParts := make(map[int]validatedMutableSQLiteBundlePartFile, len(parts))
	for _, part := range parts {
		if _, duplicate := expectedParts[part.Index]; duplicate {
			return nil, fmt.Errorf("sqlite bundle part files repeat index %d", part.Index)
		}
		expected := part.SQLiteBundlePart
		if len(manifestParts) > 0 {
			var ok bool
			expected, ok = manifestParts[part.Index]
			if !ok || part.SQLiteBundlePart != expected {
				return nil, fmt.Errorf(
					"sqlite bundle part file %d does not match the manifest",
					part.Index,
				)
			}
		}
		size, err := validateMutableSQLiteBundlePartFile(ctx, part.Index, part)
		if err != nil {
			return nil, err
		}
		expectedParts[part.Index] = validatedMutableSQLiteBundlePartFile{
			part:      expected,
			localSize: size,
		}
	}
	return expectedParts, nil
}

func validateMutableSQLiteBundlePartFile(
	ctx context.Context,
	index int,
	part SQLiteBundlePartFile,
) (int64, error) {
	if err := validateSQLiteBundlePartLimit(part.Index, part.Size, false); err != nil {
		return 0, err
	}
	source, err := os.Open(part.Path)
	if err != nil {
		return 0, fmt.Errorf("open sqlite bundle part %d: %w", index, err)
	}
	defer func() { _ = source.Close() }()
	infoBefore, err := source.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat sqlite bundle part %d: %w", index, err)
	}
	expectedSize := part.Size
	if expectedSize == -1 {
		expectedSize = infoBefore.Size()
	}
	if !infoBefore.Mode().IsRegular() || infoBefore.Size() != expectedSize {
		return 0, fmt.Errorf(
			"sqlite bundle part file %d must be a %d-byte regular file",
			index,
			expectedSize,
		)
	}
	hash := sha256.New()
	size, err := copySQLiteBundleDeclaredSize(ctx, hash, source, expectedSize)
	if err != nil {
		return 0, fmt.Errorf("validate sqlite bundle part %d: %w", index, err)
	}
	infoAfter, err := source.Stat()
	if err != nil {
		return 0, fmt.Errorf("restat sqlite bundle part %d: %w", index, err)
	}
	if !os.SameFile(infoBefore, infoAfter) || infoAfter.Size() != expectedSize || size != expectedSize {
		return 0, fmt.Errorf("sqlite bundle part file %d changed during validation", index)
	}
	expectedSHA256 := strings.TrimSpace(part.SHA256)
	if expectedSHA256 != "" &&
		!strings.EqualFold(fmt.Sprintf("%x", hash.Sum(nil)), expectedSHA256) {
		return 0, fmt.Errorf("sqlite bundle part file %d sha256 does not match the manifest", index)
	}
	return expectedSize, nil
}

func snapshotSQLiteBundlePart(
	ctx context.Context,
	tempDir string,
	index int,
	part SQLiteBundlePartFile,
) (_ *os.File, err error) {
	snapshotPath := filepath.Join(tempDir, fmt.Sprintf("part-%04d", index))
	snapshot, err := os.OpenFile(snapshotPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create sqlite bundle part snapshot %d: %w", index, err)
	}
	defer func() {
		if err != nil {
			_ = snapshot.Close()
		}
	}()
	if err := copyValidatedSQLiteBundlePart(ctx, index, part, snapshot); err != nil {
		return nil, err
	}
	if err := snapshot.Sync(); err != nil {
		return nil, fmt.Errorf("sync sqlite bundle part snapshot %d: %w", index, err)
	}
	if err := snapshot.Close(); err != nil {
		return nil, fmt.Errorf("close sqlite bundle part snapshot %d: %w", index, err)
	}
	snapshot, err = os.Open(snapshotPath)
	if err != nil {
		return nil, fmt.Errorf("reopen sqlite bundle part snapshot %d: %w", index, err)
	}
	return snapshot, nil
}

func copyValidatedSQLiteBundlePart(
	ctx context.Context,
	index int,
	part SQLiteBundlePartFile,
	dst io.Writer,
) error {
	source, err := os.Open(part.Path)
	if err != nil {
		return fmt.Errorf("open sqlite bundle part %d: %w", index, err)
	}
	defer func() { _ = source.Close() }()
	infoBefore, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat sqlite bundle part %d: %w", index, err)
	}
	if !infoBefore.Mode().IsRegular() || infoBefore.Size() != part.Size {
		return fmt.Errorf("sqlite bundle part file %d must be a %d-byte regular file", index, part.Size)
	}
	hash := sha256.New()
	size, err := copySQLiteBundleDeclaredSize(
		ctx,
		io.MultiWriter(dst, hash),
		source,
		part.Size,
	)
	if err != nil {
		return fmt.Errorf("validate sqlite bundle part %d: %w", index, err)
	}
	infoAfter, err := source.Stat()
	if err != nil {
		return fmt.Errorf("restat sqlite bundle part %d: %w", index, err)
	}
	if !os.SameFile(infoBefore, infoAfter) || infoAfter.Size() != part.Size || size != part.Size {
		return fmt.Errorf("sqlite bundle part file %d changed during validation", index)
	}
	actualSHA256 := fmt.Sprintf("%x", hash.Sum(nil))
	if !strings.EqualFold(actualSHA256, part.SHA256) {
		return fmt.Errorf("sqlite bundle part file %d sha256 does not match the manifest", index)
	}
	return nil
}

func validateSnapshotSQLiteBundleContent(
	ctx context.Context,
	manifest SQLiteBundleManifest,
	parts []validatedSnapshotSQLiteBundlePartFile,
) error {
	compressedHash := sha256.New()
	var compressedSize int64
	for index, part := range parts {
		size, err := copySQLiteBundleDeclaredSize(
			ctx,
			compressedHash,
			part.file,
			part.part.Size,
		)
		if err != nil {
			return fmt.Errorf("hash sqlite bundle compressed part %d: %w", index, err)
		}
		if size != part.part.Size {
			return fmt.Errorf("sqlite bundle compressed object does not match the manifest")
		}
		compressedSize += size
	}
	if compressedSize != manifest.CompressedObject.Size ||
		fmt.Sprintf("%x", compressedHash.Sum(nil)) != manifest.CompressedObject.SHA256 {
		return fmt.Errorf("sqlite bundle compressed object does not match the manifest")
	}
	if err := rewindValidatedSQLiteBundleFiles(parts); err != nil {
		return err
	}
	readers := make([]io.Reader, len(parts))
	for index := range parts {
		reader, err := sqliteBundleDeclaredSizeReader(
			parts[index].file,
			parts[index].part.Size,
		)
		if err != nil {
			return err
		}
		readers[index] = reader
	}
	decompressor, err := gzip.NewReader(io.MultiReader(readers...))
	if err != nil {
		return fmt.Errorf("decompress sqlite bundle snapshot: %w", err)
	}
	objectHash := sha256.New()
	objectSize, copyErr := copyWithContext(
		ctx,
		objectHash,
		io.LimitReader(decompressor, manifest.Object.Size+1),
	)
	closeErr := decompressor.Close()
	if copyErr != nil {
		return fmt.Errorf("decompress sqlite bundle snapshot: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close sqlite bundle decompressor: %w", closeErr)
	}
	if objectSize != manifest.Object.Size ||
		fmt.Sprintf("%x", objectHash.Sum(nil)) != manifest.Object.SHA256 {
		return fmt.Errorf("sqlite bundle decompressed object does not match the manifest")
	}
	return rewindValidatedSQLiteBundleFiles(parts)
}

func rewindValidatedSQLiteBundleFiles(parts []validatedSnapshotSQLiteBundlePartFile) error {
	for index, part := range parts {
		if _, err := part.file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind sqlite bundle part %d: %w", index, err)
		}
	}
	return nil
}

func closeValidatedSnapshotSQLiteBundleFiles(parts []validatedSnapshotSQLiteBundlePartFile) {
	tempDirs := map[string]struct{}{}
	for _, part := range parts {
		_ = part.file.Close()
		if part.tempDir != "" {
			tempDirs[part.tempDir] = struct{}{}
		}
	}
	for tempDir := range tempDirs {
		_ = os.RemoveAll(tempDir)
	}
}
