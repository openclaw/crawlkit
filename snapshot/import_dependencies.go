package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Generic DELETE and INSERT OR REPLACE can change rows in tables omitted by
// an incremental plan. Inspect the destination before any caller hooks mutate it.
// Dependency-aware custom callbacks retain responsibility for their own writes.
func checkIncrementalDependencies(ctx context.Context, tx *sql.Tx, plan ImportPlan, opts IncrementalImportOptions) error {
	checked := make(map[string]bool)
	for _, table := range plan.Tables {
		generic := false
		switch table.Mode {
		case TableImportSkip:
			continue
		case TableImportReplace:
			hasFiles := len(table.Table.Files) > 0 || strings.TrimSpace(table.Table.File) != ""
			generic = opts.DeleteTable == nil || (opts.ImportRow == nil && hasFiles)
		case TableImportFiles:
			generic = opts.ImportRow == nil && len(table.Files) > 0
		default:
			return fmt.Errorf("unknown table import mode %q for %s", table.Mode, table.Table.Name)
		}
		name := table.Table.Name
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
