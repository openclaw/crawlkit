//go:build darwin

package sessionowner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type FileStage string

const (
	BeforeStage        FileStage = "before_stage"
	AfterWrite         FileStage = "after_write"
	AfterSync          FileStage = "after_fsync"
	AfterRename        FileStage = "after_rename"
	AfterDirectorySync FileStage = "after_directory_fsync"
)

type FileOptions struct {
	Directory string
	BindingID string
	Fault     func(FileStage) error
}
type FileBackend struct{ options FileOptions }
type fileGuard struct {
	lock  *os.File
	inode os.FileInfo
}

func NewFileBackend(o FileOptions) (*FileBackend, error) {
	if !validID(o.BindingID) {
		return nil, fail(UnsafeStorage, "binding")
	}
	if e := secureDirectory(o.Directory); e != nil {
		return nil, e
	}
	return &FileBackend{o}, nil
}
func secureDirectory(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return fail(UnsafeStorage, "directory")
	}
	for p := dir; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fail(UnsafeStorage, "ancestor")
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || (sys.Uid != uint32(os.Getuid()) && sys.Uid != 0) || st.Mode().Perm()&0022 != 0 {
			return fail(UnsafeStorage, "ancestor_owner")
		}
		if p == dir && (sys.Uid != uint32(os.Getuid()) || st.Mode().Perm() != 0700) {
			return fail(UnsafeStorage, "directory_mode")
		}
		if p == "/" {
			break
		}
	}
	var fs syscall.Statfs_t
	if e := syscall.Statfs(dir, &fs); e != nil {
		return fail(UnsafeStorage, "filesystem")
	}
	b := make([]byte, 0, 16)
	for _, c := range fs.Fstypename {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	if string(b) != "apfs" && string(b) != "hfs" {
		return fail(UnsafeStorage, "unsupported_filesystem")
	}
	return nil
}
func safeFile(f *os.File, path string) error {
	st, e := f.Stat()
	if e != nil {
		return fail(UnsafeStorage, "file")
	}
	l, e := os.Lstat(path)
	if e != nil || !os.SameFile(st, l) || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return fail(UnsafeStorage, "file_mode")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != uint32(os.Getuid()) || sys.Nlink != 1 {
		return fail(UnsafeStorage, "file_owner")
	}
	return nil
}
func (b *FileBackend) Acquire(ctx context.Context) (*Guard, error) {
	if e := secureDirectory(b.options.Directory); e != nil {
		return nil, e
	}
	p := filepath.Join(b.options.Directory, "authority.lock")
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return nil, fail(UnsafeStorage, "lock")
	}
	if e = safeFile(f, p); e != nil {
		f.Close()
		return nil, e
	}
	for {
		e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			break
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			f.Close()
			return nil, fail(UnsafeStorage, "flock")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fail(OwnerBusy, "lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if e = safeFile(f, p); e != nil {
		f.Close()
		return nil, e
	}
	st, _ := f.Stat()
	fg := &fileGuard{f, st}
	g := NewGuard(b, func() error { defer fg.lock.Close(); return syscall.Flock(int(fg.lock.Fd()), syscall.LOCK_UN) })
	g.verify = func() error {
		current, e := os.Lstat(p)
		if e != nil || !os.SameFile(fg.inode, current) {
			return fail(UnsafeStorage, "lock_inode")
		}
		return safeFile(fg.lock, p)
	}
	return g, nil
}
func (b *FileBackend) check(g *Guard) error {
	if e := g.Check(b); e != nil {
		return e
	}
	return secureDirectory(b.options.Directory)
}

type diskEnvelope struct {
	Version  string          `json:"version"`
	Body     json.RawMessage `json:"body"`
	Checksum string          `json:"checksum"`
}

func (b *FileBackend) Load(ctx context.Context, g *Guard) (*Snapshot, error) {
	if e := b.check(g); e != nil {
		return nil, e
	}
	p := filepath.Join(b.options.Directory, "authority.json")
	f, e := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if os.IsNotExist(e) {
		return NewSnapshot(b.options.BindingID), nil
	}
	if e != nil {
		return nil, fail(UnsafeStorage, "state_open")
	}
	defer f.Close()
	if e = safeFile(f, p); e != nil {
		return nil, e
	}
	raw, e := io.ReadAll(io.LimitReader(f, HardFrameLimit+1))
	if e != nil || len(raw) > HardFrameLimit {
		return nil, fail(UnsafeStorage, "state_size")
	}
	if _, e = DecodeJSON(raw, HardFrameLimit); e != nil {
		return nil, fail(UnsafeStorage, "state_json")
	}
	var envelope diskEnvelope
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&envelope); e != nil || envelope.Version != "sessionowner-file/1" || Hash(envelope.Body) != envelope.Checksum {
		return nil, fail(UnsafeStorage, "state_checksum")
	}
	var s Snapshot
	d = json.NewDecoder(bytes.NewReader(envelope.Body))
	d.UseNumber()
	d.DisallowUnknownFields()
	if e = d.Decode(&s); e != nil {
		return nil, fail(UnsafeStorage, "state_body")
	}
	if e = s.Validate(); e != nil {
		return nil, e
	}
	if s.BindingID != b.options.BindingID {
		return nil, fail(UnsafeStorage, "state_binding")
	}
	return &s, nil
}
func (b *FileBackend) Commit(ctx context.Context, g *Guard, expected Expected, next *Snapshot, id string) (CommitReceipt, error) {
	old, e := b.Load(ctx, g)
	if e != nil {
		return CommitReceipt{}, e
	}
	copy, r, e := prepareCommit(old, next, expected, id)
	if e != nil || copy == nil {
		return r, e
	}
	fault := func(stage FileStage) error {
		if b.options.Fault != nil {
			return b.options.Fault(stage)
		}
		return nil
	}
	if e = fault(BeforeStage); e != nil {
		return CommitReceipt{OperationID: id, Certainty: "not_committed"}, e
	}
	body, _ := json.Marshal(copy)
	raw, _ := json.Marshal(diskEnvelope{"sessionowner-file/1", body, Hash(body)})
	if len(raw) > HardFrameLimit {
		return CommitReceipt{}, fail(StorageFailure, "state_size")
	}
	name := filepath.Join(b.options.Directory, "stage-"+newID())
	f, e := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return CommitReceipt{}, fail(StorageFailure, "stage")
	}
	defer f.Close()
	if e = safeFile(f, name); e != nil {
		return CommitReceipt{}, e
	}
	if e = writeAll(f, raw); e == nil {
		e = fault(AfterWrite)
	}
	if e == nil {
		e = f.Sync()
	}
	if e == nil {
		e = fault(AfterSync)
	}
	if e != nil {
		return CommitReceipt{OperationID: id, Certainty: "not_committed"}, fail(StorageFailure, "stage_write")
	}
	if e = b.check(g); e != nil {
		return CommitReceipt{}, e
	}
	target := filepath.Join(b.options.Directory, "authority.json")
	if st, e := os.Lstat(target); e == nil {
		if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			return CommitReceipt{}, fail(UnsafeStorage, "target")
		}
	} else if !os.IsNotExist(e) {
		return CommitReceipt{}, fail(UnsafeStorage, "target")
	}
	if e = os.Rename(name, target); e != nil {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, fail(StorageFailure, "rename")
	}
	if e = fault(AfterRename); e != nil {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, fail(StorageFailure, "rename_ack")
	}
	dir, e := os.Open(b.options.Directory)
	if e != nil {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, fail(StorageFailure, "directory")
	}
	e = dir.Sync()
	dir.Close()
	if e == nil {
		e = fault(AfterDirectorySync)
	}
	if e != nil {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, fail(StorageFailure, "directory_sync")
	}
	return r, nil
}
func (b *FileBackend) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	s, e := b.Load(ctx, g)
	if e != nil {
		return CommitReceipt{OperationID: id, Certainty: "uncertain"}, e
	}
	if r, ok := s.Commits[id]; ok {
		f, e := os.OpenFile(filepath.Join(b.options.Directory, "authority.json"), os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, fail(StorageFailure, "reconcile_sync")
		}
		e = f.Sync()
		f.Close()
		if e != nil {
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, fail(StorageFailure, "reconcile_sync")
		}
		dir, e := os.Open(b.options.Directory)
		if e != nil {
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, e
		}
		e = dir.Sync()
		dir.Close()
		if e != nil {
			return CommitReceipt{OperationID: id, Certainty: "uncertain"}, fail(StorageFailure, "reconcile_sync")
		}
		return r, nil
	}
	return CommitReceipt{OperationID: id, Certainty: "not_committed"}, nil
}
