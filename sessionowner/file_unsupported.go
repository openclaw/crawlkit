//go:build !darwin

package sessionowner

import "context"

// Only Darwin local APFS/HFS custody is qualified by this source candidate.
// Other platforms fail closed instead of silently omitting file guards.
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
type FileBackend struct{}

func NewFileBackend(FileOptions) (*FileBackend, error) {
	return nil, fail(UnsafeStorage, "unsupported_platform")
}
func (*FileBackend) Acquire(context.Context) (*Guard, error) {
	return nil, fail(UnsafeStorage, "unsupported_platform")
}
func (*FileBackend) Load(context.Context, *Guard) (*Snapshot, error) {
	return nil, fail(UnsafeStorage, "unsupported_platform")
}
func (*FileBackend) Commit(context.Context, *Guard, Expected, *Snapshot, string) (CommitReceipt, error) {
	return CommitReceipt{}, fail(UnsafeStorage, "unsupported_platform")
}
func (*FileBackend) Reconcile(context.Context, *Guard, string) (CommitReceipt, error) {
	return CommitReceipt{Certainty: "uncertain"}, fail(UnsafeStorage, "unsupported_platform")
}
