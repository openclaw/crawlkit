package snapshot

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
)

const ManifestName = "manifest.json"

const defaultMaxShardBytes int64 = 40 * 1024 * 1024

type ExportOptions struct {
	DB            *sql.DB
	RootDir       string
	Tables        []string
	MaxShardBytes int64
	Filter        RowFilter
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

type Sidecar struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind,omitempty"`
}

type Manifest struct {
	Version     int               `json:"version"`
	GeneratedAt time.Time         `json:"generated_at"`
	Tables      []TableManifest   `json:"tables"`
	Sidecars    []Sidecar         `json:"sidecars,omitempty"`
	Files       map[string]string `json:"files,omitempty"`
}

type TableManifest struct {
	Name          string         `json:"name"`
	File          string         `json:"file,omitempty"`
	Files         []string       `json:"files"`
	FileManifests []FileManifest `json:"file_manifests,omitempty"`
	Columns       []string       `json:"columns"`
	Rows          int            `json:"rows"`
}

type FileManifest struct {
	Path   string `json:"path"`
	Rows   int    `json:"rows"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

var ErrNoManifest = errors.New("pack manifest not found")

type TableImportMode string

const (
	TableImportSkip    TableImportMode = "skip"
	TableImportReplace TableImportMode = "replace"
	TableImportFiles   TableImportMode = "files"
)

type ImportPlan struct {
	Full   bool
	Reason string
	Tables []TableImportPlan
}

type TableImportPlan struct {
	Table  TableManifest
	Mode   TableImportMode
	Files  []FileManifest
	Reason string
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
	if opts.DB == nil {
		return Manifest{}, errors.New("db is required")
	}
	if strings.TrimSpace(opts.RootDir) == "" {
		return Manifest{}, errors.New("root dir is required")
	}
	if len(opts.Tables) == 0 {
		return Manifest{}, errors.New("at least one table is required")
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	maxShardBytes := opts.MaxShardBytes
	if maxShardBytes == 0 {
		maxShardBytes = defaultMaxShardBytes
	}
	tablesDir := filepath.Join(opts.RootDir, "tables")
	if err := os.RemoveAll(tablesDir); err != nil {
		return Manifest{}, fmt.Errorf("reset tables dir: %w", err)
	}
	if err := os.MkdirAll(tablesDir, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("create tables dir: %w", err)
	}
	manifest := Manifest{
		Version:     1,
		GeneratedAt: now().UTC(),
		Sidecars:    opts.Sidecars,
		Files:       map[string]string{"manifest": ManifestName},
	}
	for _, table := range opts.Tables {
		entry, err := exportTable(ctx, opts.DB, opts.RootDir, table, maxShardBytes, opts.Filter)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Tables = append(manifest.Tables, entry)
	}
	if err := WriteManifest(opts.RootDir, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Import(ctx context.Context, opts ImportOptions) (Manifest, error) {
	if opts.DB == nil {
		return Manifest{}, errors.New("db is required")
	}
	manifest, err := ReadManifest(opts.RootDir)
	if err != nil {
		return Manifest{}, err
	}
	deleteTables := opts.DeleteTables
	if len(deleteTables) == 0 {
		for _, table := range manifest.Tables {
			deleteTables = append(deleteTables, table.Name)
		}
	}
	tx, err := opts.DB.BeginTx(ctx, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("begin import tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if opts.BeforeImport != nil {
		if err := opts.BeforeImport(ctx, tx); err != nil {
			return Manifest{}, err
		}
	}
	for i := len(deleteTables) - 1; i >= 0; i-- {
		table := strings.TrimSpace(deleteTables[i])
		if table == "" {
			continue
		}
		if opts.DeleteTable != nil {
			if err := opts.DeleteTable(ctx, tx, table); err != nil {
				return Manifest{}, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, "delete from "+store.QuoteIdent(table)); err != nil {
			return Manifest{}, fmt.Errorf("clear table %s: %w", table, err)
		}
	}
	for _, table := range manifest.Tables {
		rows, err := importTable(ctx, tx, opts.RootDir, table, opts.Filter, opts.ImportRow, opts.Progress)
		if err != nil {
			return Manifest{}, err
		}
		reportImportProgress(opts.Progress, ImportProgress{Phase: "table_done", Table: table.Name, Rows: rows, TotalRows: table.Rows})
	}
	if opts.AfterImport != nil {
		if err := opts.AfterImport(ctx, tx); err != nil {
			return Manifest{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Manifest{}, fmt.Errorf("commit import tx: %w", err)
	}
	committed = true
	return manifest, nil
}

func PlanIncrementalImport(previous, current Manifest) ImportPlan {
	if current.Version != previous.Version {
		return ImportPlan{Full: true, Reason: "manifest version changed"}
	}
	previousTables := make(map[string]TableManifest, len(previous.Tables))
	for _, table := range previous.Tables {
		previousTables[table.Name] = table
	}
	currentTables := make(map[string]TableManifest, len(current.Tables))
	for _, table := range current.Tables {
		currentTables[table.Name] = table
	}
	for name := range previousTables {
		if _, ok := currentTables[name]; !ok {
			return ImportPlan{Full: true, Reason: "table removed: " + name}
		}
	}
	plan := ImportPlan{}
	for _, table := range current.Tables {
		previousTable, ok := previousTables[table.Name]
		if !ok {
			plan.Tables = append(plan.Tables, TableImportPlan{
				Table:  table,
				Mode:   TableImportReplace,
				Files:  tableFileManifests(table),
				Reason: "new table",
			})
			continue
		}
		tablePlan := planTableIncrement(previousTable, table)
		plan.Tables = append(plan.Tables, tablePlan)
	}
	return plan
}

func (p ImportPlan) Changed() bool {
	if p.Full {
		return true
	}
	for _, table := range p.Tables {
		if table.Mode != TableImportSkip {
			return true
		}
	}
	return false
}

func ImportIncremental(ctx context.Context, opts IncrementalImportOptions) (Manifest, ImportPlan, error) {
	if opts.DB == nil {
		return Manifest{}, ImportPlan{}, errors.New("db is required")
	}
	current := opts.Current
	var err error
	if len(current.Tables) == 0 {
		current, err = ReadManifest(opts.RootDir)
		if err != nil {
			return Manifest{}, ImportPlan{}, err
		}
	}
	plan := opts.Plan
	if len(plan.Tables) == 0 && !plan.Full && plan.Reason == "" {
		plan = PlanIncrementalImport(opts.Previous, current)
	}
	if plan.Full {
		return Manifest{}, plan, errors.New("incremental import requires a non-full plan: " + plan.Reason)
	}
	if !plan.Changed() {
		return current, plan, nil
	}
	tx, err := opts.DB.BeginTx(ctx, nil)
	if err != nil {
		return Manifest{}, plan, fmt.Errorf("begin incremental import tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if opts.BeforeImport != nil {
		if err := opts.BeforeImport(ctx, tx); err != nil {
			return Manifest{}, plan, err
		}
	}
	for _, tablePlan := range plan.Tables {
		switch tablePlan.Mode {
		case TableImportSkip:
			continue
		case TableImportReplace:
			if err := deleteImportTable(ctx, tx, tablePlan.Table.Name, opts.DeleteTable); err != nil {
				return Manifest{}, plan, err
			}
			rows, err := importTable(ctx, tx, opts.RootDir, tablePlan.Table, opts.Filter, opts.ImportRow, opts.Progress)
			if err != nil {
				return Manifest{}, plan, err
			}
			reportImportProgress(opts.Progress, ImportProgress{Phase: "table_done", Table: tablePlan.Table.Name, Rows: rows, TotalRows: tablePlan.Table.Rows})
		case TableImportFiles:
			table := tablePlan.Table
			table.File = ""
			table.Files = fileManifestPaths(tablePlan.Files)
			table.FileManifests = tablePlan.Files
			table.Rows = fileManifestRows(tablePlan.Files)
			rows, err := importTable(ctx, tx, opts.RootDir, table, opts.Filter, opts.ImportRow, opts.Progress)
			if err != nil {
				return Manifest{}, plan, err
			}
			reportImportProgress(opts.Progress, ImportProgress{Phase: "table_done", Table: tablePlan.Table.Name, Rows: rows, TotalRows: table.Rows})
		default:
			return Manifest{}, plan, fmt.Errorf("unknown table import mode %q for %s", tablePlan.Mode, tablePlan.Table.Name)
		}
	}
	if opts.AfterImport != nil {
		if err := opts.AfterImport(ctx, tx); err != nil {
			return Manifest{}, plan, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Manifest{}, plan, fmt.Errorf("commit incremental import tx: %w", err)
	}
	committed = true
	return current, plan, nil
}

func ReadManifest(rootDir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(rootDir, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrNoManifest
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return manifest, nil
}

func WriteManifest(rootDir string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return fmt.Errorf("create root dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ManifestName), data, 0o600); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func exportTable(ctx context.Context, db *sql.DB, rootDir, table string, maxShardBytes int64, filter RowFilter) (TableManifest, error) {
	relDir, err := tableShardDir(table)
	if err != nil {
		return TableManifest{}, err
	}
	rows, err := db.QueryContext(ctx, "select * from "+store.QuoteIdent(table))
	if err != nil {
		return TableManifest{}, fmt.Errorf("query table %s: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return TableManifest{}, err
	}
	writer := &shardWriter{
		rootDir:       rootDir,
		relDir:        relDir,
		maxShardBytes: maxShardBytes,
	}
	if err := os.MkdirAll(filepath.Join(rootDir, filepath.FromSlash(relDir)), 0o755); err != nil {
		return TableManifest{}, fmt.Errorf("create table dir %s: %w", table, err)
	}
	defer writer.close()
	enc := json.NewEncoder(writer)
	count := 0
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return TableManifest{}, fmt.Errorf("scan table %s: %w", table, err)
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = exportValue(values[i])
		}
		if filter != nil {
			keep, err := filter(table, row)
			if err != nil {
				return TableManifest{}, fmt.Errorf("filter table %s: %w", table, err)
			}
			if !keep {
				continue
			}
		}
		if err := writer.rotateIfNeeded(); err != nil {
			return TableManifest{}, err
		}
		if err := enc.Encode(row); err != nil {
			return TableManifest{}, fmt.Errorf("encode table %s: %w", table, err)
		}
		count++
		if err := writer.finishRow(); err != nil {
			return TableManifest{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return TableManifest{}, err
	}
	if err := writer.close(); err != nil {
		return TableManifest{}, err
	}
	return TableManifest{Name: table, Files: writer.files, FileManifests: writer.fileManifests, Columns: cols, Rows: count}, nil
}

func importTable(ctx context.Context, tx *sql.Tx, rootDir string, table TableManifest, filter RowFilter, importRow RowImportFunc, progress func(ImportProgress)) (int, error) {
	files := table.Files
	if len(files) == 0 && strings.TrimSpace(table.File) != "" {
		files = []string{table.File}
	}
	if len(files) == 0 {
		return 0, nil
	}
	reportImportProgress(progress, ImportProgress{Phase: "table_start", Table: table.Name, FileCount: len(files), TotalRows: table.Rows})
	totalRows := 0
	for index, rel := range files {
		path := filepath.Join(rootDir, filepath.FromSlash(rel))
		file, err := os.Open(path)
		if err != nil {
			return totalRows, fmt.Errorf("open %s: %w", rel, err)
		}
		fileProgress := ImportProgress{Phase: "file_start", Table: table.Name, File: rel, FileIndex: index + 1, FileCount: len(files), TotalRows: table.Rows}
		reportImportProgress(progress, fileProgress)
		rows, err := importJSONLGzip(ctx, tx, file, table.Name, filter, importRow)
		if err != nil {
			_ = file.Close()
			return totalRows, err
		}
		if err := file.Close(); err != nil {
			return totalRows, fmt.Errorf("close %s: %w", rel, err)
		}
		totalRows += rows
		fileProgress.Phase = "file_done"
		fileProgress.Rows = rows
		reportImportProgress(progress, fileProgress)
	}
	return totalRows, nil
}

func importJSONLGzip(ctx context.Context, tx *sql.Tx, reader io.Reader, table string, filter RowFilter, importRow RowImportFunc) (int, error) {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return 0, fmt.Errorf("open gzip for %s: %w", table, err)
	}
	defer gz.Close()
	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	rows := 0
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return rows, fmt.Errorf("decode %s row: %w", table, err)
		}
		if len(row) == 0 {
			continue
		}
		if filter != nil {
			keep, err := filter(table, row)
			if err != nil {
				return rows, fmt.Errorf("filter %s row: %w", table, err)
			}
			if !keep {
				continue
			}
		}
		importFunc := importRow
		if importFunc == nil {
			importFunc = insertRow
		}
		if err := importFunc(ctx, tx, table, row); err != nil {
			return rows, err
		}
		rows++
	}
	if err := scanner.Err(); err != nil {
		return rows, fmt.Errorf("scan %s rows: %w", table, err)
	}
	return rows, nil
}

func reportImportProgress(progress func(ImportProgress), event ImportProgress) {
	if progress != nil {
		progress(event)
	}
}

func deleteImportTable(ctx context.Context, tx *sql.Tx, table string, deleteTable DeleteFunc) error {
	if deleteTable != nil {
		return deleteTable(ctx, tx, table)
	}
	if _, err := tx.ExecContext(ctx, "delete from "+store.QuoteIdent(table)); err != nil {
		return fmt.Errorf("clear table %s: %w", table, err)
	}
	return nil
}

func insertRow(ctx context.Context, tx *sql.Tx, table string, row map[string]any) error {
	cols := make([]string, 0, len(row))
	for col := range row {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	quoted := make([]string, 0, len(cols))
	holders := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, col := range cols {
		quoted = append(quoted, store.QuoteIdent(col))
		holders = append(holders, "?")
		args = append(args, row[col])
	}
	stmt := fmt.Sprintf(
		"insert or replace into %s(%s) values(%s)",
		store.QuoteIdent(table),
		strings.Join(quoted, ","),
		strings.Join(holders, ","),
	)
	if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("insert %s row: %w", table, err)
	}
	return nil
}

type shardWriter struct {
	rootDir       string
	relDir        string
	maxShardBytes int64
	nextShard     int
	rowsInShard   int
	files         []string
	fileManifests []FileManifest
	currentRel    string
	file          *os.File
	counter       *countingWriter
	hasher        hash.Hash
	gz            *gzip.Writer
}

func (w *shardWriter) Write(p []byte) (int, error) {
	if w.gz == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	return w.gz.Write(p)
}

func (w *shardWriter) open() error {
	rel := filepath.ToSlash(filepath.Join(w.relDir, fmt.Sprintf("%06d.jsonl.gz", w.nextShard)))
	path := filepath.Join(w.rootDir, filepath.FromSlash(rel))
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", rel, err)
	}
	w.nextShard++
	w.rowsInShard = 0
	w.files = append(w.files, rel)
	w.currentRel = rel
	w.file = file
	w.hasher = sha256.New()
	w.counter = &countingWriter{w: io.MultiWriter(file, w.hasher)}
	w.gz = gzip.NewWriter(w.counter)
	return nil
}

func (w *shardWriter) rotateIfNeeded() error {
	if w.maxShardBytes <= 0 || w.rowsInShard == 0 || w.counter == nil || w.counter.n < w.maxShardBytes {
		return nil
	}
	if err := w.close(); err != nil {
		return err
	}
	return w.open()
}

func (w *shardWriter) finishRow() error {
	w.rowsInShard++
	if w.maxShardBytes > 1024*1024 && w.rowsInShard%1024 != 0 {
		return nil
	}
	if w.gz == nil {
		return nil
	}
	return w.gz.Flush()
}

func (w *shardWriter) close() error {
	var closeErr error
	if w.gz != nil {
		if err := w.gz.Close(); err != nil {
			closeErr = err
		}
		w.gz = nil
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		w.file = nil
	}
	if closeErr != nil {
		return fmt.Errorf("close shard: %w", closeErr)
	}
	if w.currentRel != "" && w.counter != nil && w.hasher != nil {
		w.fileManifests = append(w.fileManifests, FileManifest{
			Path:   w.currentRel,
			Rows:   w.rowsInShard,
			Size:   w.counter.n,
			SHA256: hex.EncodeToString(w.hasher.Sum(nil)),
		})
	}
	w.currentRel = ""
	w.counter = nil
	w.hasher = nil
	return nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

func exportValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}

func planTableIncrement(previous, current TableManifest) TableImportPlan {
	if !sameStrings(previous.Columns, current.Columns) {
		return TableImportPlan{Table: current, Mode: TableImportReplace, Files: tableFileManifests(current), Reason: "columns changed"}
	}
	previousFiles := tableFileManifests(previous)
	currentFiles := tableFileManifests(current)
	if len(previousFiles) == 0 && len(currentFiles) == 0 {
		return TableImportPlan{Table: current, Mode: TableImportSkip, Reason: "unchanged"}
	}
	if !allFilesHaveFingerprints(previousFiles) || !allFilesHaveFingerprints(currentFiles) {
		return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "missing file fingerprints"}
	}
	if sameFileManifests(previousFiles, currentFiles) {
		return TableImportPlan{Table: current, Mode: TableImportSkip, Reason: "unchanged"}
	}
	if len(currentFiles) < len(previousFiles) {
		return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "files removed"}
	}
	for i := 0; i < len(previousFiles)-1; i++ {
		if !sameFileManifest(previousFiles[i], currentFiles[i]) {
			return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "non-tail file changed"}
		}
	}
	changed := make([]FileManifest, 0, len(currentFiles)-len(previousFiles)+1)
	if len(previousFiles) > 0 {
		oldTail := previousFiles[len(previousFiles)-1]
		newTail := currentFiles[len(previousFiles)-1]
		if oldTail.Path != newTail.Path {
			return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "tail path changed"}
		}
		if !sameFileManifest(oldTail, newTail) {
			return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "tail file changed"}
		}
	}
	for i := len(previousFiles); i < len(currentFiles); i++ {
		changed = append(changed, currentFiles[i])
	}
	if len(changed) == 0 {
		return TableImportPlan{Table: current, Mode: TableImportSkip, Reason: "unchanged"}
	}
	return TableImportPlan{Table: current, Mode: TableImportFiles, Files: changed, Reason: "tail files changed"}
}

func tableShardDir(table string) (string, error) {
	table = strings.TrimSpace(table)
	if table == "" {
		return "", fmt.Errorf("table name is required")
	}
	if filepath.IsAbs(table) || strings.ContainsAny(table, `/\`) || table == "." || table == ".." || strings.Contains(table, "\x00") {
		return "", fmt.Errorf("table name %q is not safe for snapshot paths", table)
	}
	return filepath.ToSlash(filepath.Join("tables", table)), nil
}

func tableFileManifests(table TableManifest) []FileManifest {
	if len(table.FileManifests) > 0 {
		out := make([]FileManifest, len(table.FileManifests))
		copy(out, table.FileManifests)
		return out
	}
	files := table.Files
	if len(files) == 0 && strings.TrimSpace(table.File) != "" {
		files = []string{table.File}
	}
	out := make([]FileManifest, 0, len(files))
	for _, file := range files {
		out = append(out, FileManifest{Path: file})
	}
	return out
}

func allFilesHaveFingerprints(files []FileManifest) bool {
	for _, file := range files {
		if file.Path == "" || file.SHA256 == "" {
			return false
		}
	}
	return true
}

func sameFileManifests(a, b []FileManifest) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameFileManifest(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameFileManifest(a, b FileManifest) bool {
	return a.Path == b.Path && a.Rows == b.Rows && a.Size == b.Size && a.SHA256 == b.SHA256
}

func fileManifestPaths(files []FileManifest) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func fileManifestRows(files []FileManifest) int {
	rows := 0
	for _, file := range files {
		rows += file.Rows
	}
	return rows
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
