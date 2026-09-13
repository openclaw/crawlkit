package snapshot

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/openclaw/crawlkit/store"
)

func exportTable(ctx context.Context, tx *sql.Tx, root *os.Root, generation, table string, maxShardBytes int64, filter RowFilter, filterTx RowFilterTx, created func(string)) (TableManifest, error) {
	relDir, err := tableShardDir(table)
	if err != nil {
		return TableManifest{}, err
	}
	relDir = generation + "/" + table
	rows, err := tx.QueryContext(ctx, "select * from "+store.QuoteIdent(table))
	if err != nil {
		return TableManifest{}, fmt.Errorf("query table %s: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return TableManifest{}, err
	}
	writer := &shardWriter{
		root:          root,
		relDir:        relDir,
		maxShardBytes: maxShardBytes,
		created:       created,
	}
	if err := root.MkdirAll(filepath.FromSlash(relDir), 0o755); err != nil {
		return TableManifest{}, fmt.Errorf("create table dir %s: %w", table, err)
	}
	defer writer.close()
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
		blobs := make(map[string]string)
		for i, col := range cols {
			row[col] = exportValue(values[i])
			if blob, ok := values[i].([]byte); ok {
				blobs[col] = string(blob)
			}
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
		if filterTx != nil {
			keep, err := filterTx(ctx, tx, table, row)
			if err != nil {
				return TableManifest{}, fmt.Errorf("filter table %s in export transaction: %w", table, err)
			}
			if !keep {
				continue
			}
		}
		data, err := encodeSnapshotRow(row, blobs)
		if err != nil {
			return TableManifest{}, fmt.Errorf("encode table %s: %w", table, err)
		}
		if err := writer.rotateIfNeeded(); err != nil {
			return TableManifest{}, err
		}
		if _, err := writer.Write(data); err != nil {
			return TableManifest{}, fmt.Errorf("write table %s: %w", table, err)
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

type shardWriter struct {
	root          *os.Root
	created       func(string)
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
	file, err := w.root.OpenFile(filepath.FromSlash(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", rel, err)
	}
	w.created(rel)
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
		if err := w.file.Sync(); err != nil && closeErr == nil {
			closeErr = err
		}
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
