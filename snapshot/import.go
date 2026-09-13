package snapshot

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/openclaw/crawlkit/store"
)

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
	currentTables := make(map[string]TableManifest, len(current.Tables))
	for _, table := range current.Tables {
		if _, exists := currentTables[table.Name]; exists {
			return Manifest{}, ImportPlan{}, fmt.Errorf("duplicate table %q in the current manifest", table.Name)
		}
		if _, err := importFileManifests(table); err != nil {
			return Manifest{}, ImportPlan{}, err
		}
		currentTables[table.Name] = table
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
	activeTables := make(map[string]bool, len(plan.Tables))
	for _, tablePlan := range plan.Tables {
		if tablePlan.Mode == TableImportSkip {
			continue
		}
		if activeTables[tablePlan.Table.Name] {
			return Manifest{}, plan, fmt.Errorf("duplicate active plan for table %q", tablePlan.Table.Name)
		}
		activeTables[tablePlan.Table.Name] = true
	}
	work, err := prepareIncrementalWork(plan, currentTables)
	if err != nil {
		return Manifest{}, plan, err
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
	if err := checkIncrementalDependencies(ctx, tx, work, opts); err != nil {
		return Manifest{}, plan, err
	}
	if opts.BeforeImport != nil {
		if err := opts.BeforeImport(ctx, tx); err != nil {
			return Manifest{}, plan, err
		}
	}
	for _, item := range work {
		table := item.table
		if item.mode == TableImportReplace {
			if err := deleteImportTable(ctx, tx, table.Name, opts.DeleteTable); err != nil {
				return Manifest{}, plan, err
			}
		}
		rows, err := importTable(ctx, tx, opts.RootDir, table, opts.Filter, opts.ImportRow, opts.Progress)
		if err != nil {
			return Manifest{}, plan, err
		}
		reportImportProgress(opts.Progress, ImportProgress{Phase: "table_done", Table: table.Name, Rows: rows, TotalRows: table.Rows})
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

func importTable(ctx context.Context, tx *sql.Tx, rootDir string, table TableManifest, filter RowFilter, importRow RowImportFunc, progress func(ImportProgress)) (int, error) {
	files, err := importFileManifests(table)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	reportImportProgress(progress, ImportProgress{Phase: "table_start", Table: table.Name, FileCount: len(files), TotalRows: table.Rows})
	totalRows := 0
	for index, entry := range files {
		rel := entry.Path
		path, err := confinedSnapshotFile(rootDir, rel)
		if err != nil {
			return totalRows, err
		}
		file, err := os.Open(path)
		if err != nil {
			return totalRows, fmt.Errorf("open %s: %w", rel, err)
		}
		fileProgress := ImportProgress{Phase: "file_start", Table: table.Name, File: rel, FileIndex: index + 1, FileCount: len(files), TotalRows: table.Rows}
		reportImportProgress(progress, fileProgress)
		var expected *FileManifest
		if len(table.FileManifests) > 0 {
			expected = &entry
		}
		rows, err := importJSONLGzip(ctx, tx, file, table.Name, filter, importRow, expected)
		if err != nil {
			_ = file.Close()
			return totalRows, fmt.Errorf("import %s: %w", rel, err)
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

func importJSONLGzip(ctx context.Context, tx *sql.Tx, reader io.Reader, table string, filter RowFilter, importRow RowImportFunc, expected *FileManifest) (int, error) {
	hasher := sha256.New()
	counter := &countingWriter{w: hasher}
	if expected != nil {
		reader = io.TeeReader(reader, counter)
	}
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return 0, fmt.Errorf("open gzip for %s: %w", table, err)
	}
	defer gz.Close()
	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	rows := 0
	decodedRows := 0
	for scanner.Scan() {
		row, err := decodeSnapshotRow(scanner.Bytes(), false)
		if err != nil {
			return rows, fmt.Errorf("decode %s row: %w", table, err)
		}
		decodedRows++
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
	// Scan through gzip EOF: Close alone does not verify the gzip checksum.
	if err := scanner.Err(); err != nil {
		return rows, fmt.Errorf("scan %s rows: %w", table, err)
	}
	if expected != nil {
		if expected.Size != 0 && counter.n != expected.Size {
			return rows, fmt.Errorf("compressed size mismatch: got %d, want %d", counter.n, expected.Size)
		}
		if expected.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expected.SHA256) {
			return rows, errors.New("compressed SHA256 mismatch")
		}
		if decodedRows != expected.Rows {
			return rows, fmt.Errorf("row count mismatch: got %d, want %d", decodedRows, expected.Rows)
		}
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

func selectImportFiles(plan TableImportPlan) (TableManifest, error) {
	table := plan.Table
	files, err := importFileManifests(table)
	if err != nil {
		return TableManifest{}, err
	}
	modern := len(table.FileManifests) > 0
	byPath := make(map[string]FileManifest, len(files))
	for _, file := range files {
		key, _ := confinedSnapshotFile(".", file.Path) // Validated above.
		byPath[key] = file
	}
	for _, file := range plan.Files {
		key, err := confinedSnapshotFile(".", file.Path)
		if err != nil {
			return TableManifest{}, fmt.Errorf("table %s: %w", table.Name, err)
		}
		expected, ok := byPath[key]
		expected.Path = file.Path
		if !ok || (modern && !sameFileManifest(file, expected)) {
			return TableManifest{}, fmt.Errorf("table %s: inconsistent planned file manifest for %q", table.Name, file.Path)
		}
		delete(byPath, key)
	}
	if modern {
		table.FileManifests = plan.Files
	}
	// Legacy plans synthesize zero-valued metadata; those rows are unknown.
	table.File = ""
	table.Files = fileManifestPaths(plan.Files)
	table.Rows = fileManifestRows(plan.Files)
	return table, nil
}
