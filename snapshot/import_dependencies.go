package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type tableImportWork struct {
	table TableManifest
	mode  TableImportMode
	files []FileManifest
}

func prepareIncrementalWork(plan ImportPlan, current map[string]TableManifest) ([]tableImportWork, error) {
	work := make([]tableImportWork, 0, len(plan.Tables))
	for _, planned := range plan.Tables {
		switch planned.Mode {
		case TableImportSkip:
			continue
		case TableImportReplace, TableImportFiles:
		default:
			return nil, fmt.Errorf("unknown table import mode %q for %s", planned.Mode, planned.Table.Name)
		}
		table, ok := current[planned.Table.Name]
		if !ok {
			return nil, fmt.Errorf("planned table %q is not in the current manifest", planned.Table.Name)
		}
		// Plans select work; only Current supplies its authoritative metadata.
		planned.Table = table
		if planned.Mode == TableImportFiles {
			var err error
			table, err = selectImportFiles(planned)
			if err != nil {
				return nil, err
			}
		}
		files, err := importFileManifests(table)
		if err != nil {
			return nil, err
		}
		// Own the operative file slices so hooks cannot change the validated
		// selection through caller inputs. Keep legacy metadata genuinely absent.
		table.File = ""
		table.Files = fileManifestPaths(files)
		if len(table.FileManifests) > 0 {
			table.FileManifests = files
		} else {
			table.FileManifests = nil
		}
		work = append(work, tableImportWork{table: table, mode: planned.Mode, files: files})
	}
	return work, nil
}

// Generic DELETE and INSERT OR REPLACE can change rows in tables omitted by
// an incremental plan. Inspect the destination before any caller hooks mutate it.
// Dependency-aware custom callbacks retain responsibility for their own writes.
func checkIncrementalDependencies(ctx context.Context, tx *sql.Tx, work []tableImportWork, opts IncrementalImportOptions) error {
	checked := make(map[string]bool)
	for _, item := range work {
		generic := false
		switch item.mode {
		case TableImportReplace:
			generic = opts.DeleteTable == nil || (opts.ImportRow == nil && len(item.files) > 0)
		case TableImportFiles:
			generic = opts.ImportRow == nil && len(item.files) > 0
		default:
			return fmt.Errorf("unknown table import mode %q for %s", item.mode, item.table.Name)
		}
		name := item.table.Name
		if !generic || checked[name] {
			continue
		}
		if err := checkGenericImportTable(ctx, tx, name); err != nil {
			return err
		}
		checked[name] = true
	}
	return nil
}

func checkGenericImportTable(ctx context.Context, tx *sql.Tx, table string) error {
	// The generic path supports main-schema tables. Refuse shadowed/other-schema
	// targets rather than checking one table and mutating another.
	var mainTable, shadowed bool
	if err := tx.QueryRowContext(ctx, `select
exists(select 1 from main.sqlite_schema where type='table' and name=? collate nocase),
exists(select 1 from temp.sqlite_schema where type in ('table','view') and name=? collate nocase)`,
		table, table).Scan(&mainTable, &shadowed); err != nil {
		return fmt.Errorf("inspect incremental target %s: %w", table, err)
	}
	if !mainTable || shadowed {
		return fmt.Errorf("incremental table %s requires an unshadowed main-schema table or dependency-aware custom callbacks", table)
	}

	var child, action string
	err := tx.QueryRowContext(ctx, `select s.name, f."on_delete"
from main.sqlite_schema as s, pragma_foreign_key_list(s.name, 'main') as f
where s.type='table' and f."table"=? collate nocase
and f."on_delete" in ('CASCADE','SET NULL','SET DEFAULT') limit 1`, table).Scan(&child, &action)
	if err == nil {
		return fmt.Errorf("incremental table %s has unsupported ON DELETE %s dependency from %s; use dependency-aware custom callbacks", table, action, child)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect incremental foreign keys for %s: %w", table, err)
	}

	var trigger string
	err = tx.QueryRowContext(ctx, `select name from (
select name, tbl_name from main.sqlite_schema where type='trigger'
union all select name, tbl_name from temp.sqlite_schema where type='trigger'
) where tbl_name=? collate nocase limit 1`, table).Scan(&trigger)
	if err == nil {
		return fmt.Errorf("incremental table %s has unsupported trigger %s; use dependency-aware custom callbacks", table, trigger)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect incremental triggers for %s: %w", table, err)
	}
	return nil
}
