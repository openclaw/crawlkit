//go:build darwin

package sessionowner

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func NewProtectedCandidateFiles(directory, binding string) (*ProtectedCandidateFiles, error) {
	if !validID(binding) {
		return nil, fail(UnsafeStorage, "candidate_binding")
	}
	if e := secureDirectory(directory); e != nil {
		return nil, e
	}
	return &ProtectedCandidateFiles{directory, binding}, nil
}
func (f *ProtectedCandidateFiles) path(ref string) (string, error) {
	if !opaqueCandidateRef(ref) {
		return "", fail(UnsafeStorage, "candidate_reference")
	}
	return filepath.Join(f.directory, ref+".candidate"), nil
}
func (f *ProtectedCandidateFiles) Write(ctx context.Context, scope CandidateScope, raw []byte, expires time.Time, max int) (CandidateReference, error) {
	if ctx.Err() != nil {
		return CandidateReference{}, fail(Canceled, "candidate_write")
	}
	if scope.BindingID != f.binding || !validCandidateScope(scope) || expires.IsZero() || expires.Year() < 1 || expires.Year() > 9999 || len(raw) < 1 || max < 1 || max > HardFrameLimit || CheckBytes(raw, max) != nil {
		return CandidateReference{}, fail(ProtocolMismatch, "candidate_publish")
	}
	if e := secureDirectory(f.directory); e != nil {
		return CandidateReference{}, e
	}
	ref := newID()
	target, _ := f.path(ref)
	header := candidateFileHeader{"candidate-stage/1", scope, expires, len(raw), Hash(raw)}
	metadata, e := json.Marshal(header)
	if e != nil || len(metadata) > candidateHeaderCap {
		return CandidateReference{}, fail(PayloadTooLarge, "candidate_metadata")
	}
	file, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return CandidateReference{}, fail(UnsafeStorage, "candidate_create")
	}
	defer file.Close()
	if e = safeFile(file, target); e != nil {
		return CandidateReference{}, e
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(metadata)))
	for _, part := range [][]byte{[]byte(candidateFileMagic), size[:], metadata, raw} {
		if ctx.Err() != nil {
			return CandidateReference{}, fail(Canceled, "candidate_write")
		}
		if e = writeAll(file, part); e != nil {
			return CandidateReference{}, fail(StorageFailure, "candidate_write")
		}
	}
	if e = file.Sync(); e != nil {
		return CandidateReference{}, fail(StorageFailure, "candidate_sync")
	}
	dir, e := os.Open(f.directory)
	if e != nil {
		return CandidateReference{}, fail(UnsafeStorage, "candidate_directory")
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return CandidateReference{}, fail(StorageFailure, "candidate_directory_sync")
	}
	if ctx.Err() != nil {
		return CandidateReference{}, fail(Canceled, "candidate_write")
	}
	return CandidateReference{ref, len(raw), header.Digest}, nil
}
func (f *ProtectedCandidateFiles) Read(ctx context.Context, scope CandidateScope, ref string, max int) (CandidateMaterial, error) {
	if ctx.Err() != nil {
		return CandidateMaterial{}, fail(Canceled, "candidate_read")
	}
	if max < 1 || max > HardFrameLimit || scope.BindingID != f.binding || !validCandidateScope(scope) {
		return CandidateMaterial{}, fail(PermissionDenied, "candidate_scope")
	}
	if e := secureDirectory(f.directory); e != nil {
		return CandidateMaterial{}, e
	}
	path, e := f.path(ref)
	if e != nil {
		return CandidateMaterial{}, e
	}
	st, statErr := os.Lstat(path)
	if statErr != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return CandidateMaterial{}, fail(UnsafeStorage, "candidate_file_type")
	}
	file, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if e != nil {
		return CandidateMaterial{}, fail(UnsafeStorage, "candidate_open")
	}
	defer file.Close()
	if e = safeFile(file, path); e != nil {
		return CandidateMaterial{}, e
	}
	magic := make([]byte, len(candidateFileMagic))
	if _, e = io.ReadFull(file, magic); e != nil || string(magic) != candidateFileMagic {
		return CandidateMaterial{}, fail(UnsafeStorage, "candidate_header")
	}
	var size [4]byte
	if _, e = io.ReadFull(file, size[:]); e != nil {
		return CandidateMaterial{}, fail(UnsafeStorage, "candidate_header")
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > candidateHeaderCap {
		return CandidateMaterial{}, fail(UnsafeStorage, "candidate_header_size")
	}
	metadata := make([]byte, n)
	if _, e = io.ReadFull(file, metadata); e != nil {
		return CandidateMaterial{}, fail(UnsafeStorage, "candidate_header")
	}
	decodedMetadata, decodeErr := DecodeJSON(metadata, candidateHeaderCap)
	if decodeErr != nil || !exactCandidateMetadata(decodedMetadata) {
		return CandidateMaterial{}, fail(UnsafeStorage, "candidate_metadata")
	}

	var h candidateFileHeader
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&h); e != nil || h.Version != "candidate-stage/1" || !candidateScopeEqual(h.Scope, scope) || h.Bytes < 1 || h.Bytes > max || h.ExpiresAt.IsZero() {
		return CandidateMaterial{}, fail(PermissionDenied, "candidate_metadata_binding")
	}
	raw, e := io.ReadAll(io.LimitReader(file, int64(max)+1))
	if e != nil || len(raw) != h.Bytes || Hash(raw) != h.Digest {
		return CandidateMaterial{}, fail(UnsafeStorage, "candidate_bytes")
	}
	if e = safeFile(file, path); e != nil {
		return CandidateMaterial{}, e
	}
	if ctx.Err() != nil {
		return CandidateMaterial{}, fail(Canceled, "candidate_read")
	}
	return CandidateMaterial{Raw: raw, Scope: h.Scope, ExpiresAt: h.ExpiresAt}, nil
}
