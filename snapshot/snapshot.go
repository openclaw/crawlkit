package snapshot

import (
	"context"
	"database/sql"
	"time"
)

const ManifestName = "manifest.json"

const defaultMaxShardBytes int64 = 40 * 1024 * 1024

type ExportOptions struct {
	DB *sql.DB
	// ReadTx is optional and caller-owned. Export never commits or rolls it back.
	ReadTx        *sql.Tx
	RootDir       string
	Tables        []string
	MaxShardBytes int64
	Filter        RowFilter
	FilterTx      RowFilterTx
	Sidecars      []Sidecar
	Now           func() time.Time
}

type ImportOptions struct {
	DB           *sql.DB
	RootDir      string
	DeleteTables []string
	DeleteTable  DeleteFunc
	Filter       RowFilter
	ImportRow    RowImportFunc
	Progress     func(ImportProgress)
	BeforeImport func(context.Context, *sql.Tx) error
	AfterImport  func(context.Context, *sql.Tx) error
}

type RowFilter func(table string, row map[string]any) (bool, error)

// RowFilterTx runs after Filter for admitted rows, using the export transaction.
// It must not mutate the database or end the transaction.
type RowFilterTx func(ctx context.Context, tx *sql.Tx, table string, row map[string]any) (bool, error)

type RowImportFunc func(ctx context.Context, tx *sql.Tx, table string, row map[string]any) error

type DeleteFunc func(ctx context.Context, tx *sql.Tx, table string) error

type ImportProgress struct {
	Phase     string
	Table     string
	File      string
	FileIndex int
	FileCount int
	Rows      int
	TotalRows int
}

type IncrementalImportOptions struct {
	DB           *sql.DB
	RootDir      string
	Previous     Manifest
	Current      Manifest
	Plan         ImportPlan
	DeleteTable  DeleteFunc
	Filter       RowFilter
	ImportRow    RowImportFunc
	Progress     func(ImportProgress)
	BeforeImport func(context.Context, *sql.Tx) error
	AfterImport  func(context.Context, *sql.Tx) error
}

func Export(ctx context.Context, opts ExportOptions) (Manifest, error) {
	return exportSnapshot(ctx, opts)
}
