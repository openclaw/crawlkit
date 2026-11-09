package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteLockWaitHonorsContextDeadline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	holder, err := Open(ctx, Options{Path: path, Schema: `create table things(value text)`})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	waiter, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Close()

	tx, err := holder.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `insert into things values('held')`); err != nil {
		t.Fatal(err)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = waiter.DB().ExecContext(writeCtx, `insert into things values('waiting')`)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write error = %v, want deadline exceeded", err)
	}
	// Leave ample scheduling headroom while rejecting the five-second busy wait.
	if elapsed > 2*time.Second {
		t.Fatalf("blocked write took %v after a 100ms deadline", elapsed)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := waiter.DB().ExecContext(ctx, `insert into things values('retry')`); err != nil {
		t.Fatalf("write after releasing lock: %v", err)
	}
	var count int
	if err := waiter.DB().QueryRowContext(ctx, `select count(*) from things`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want only the retry", count)
	}
}

func TestOpenFinishedContextDoesNotCreateDatabase(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelDeadline()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"cancelled", cancelled, context.Canceled},
		{"expired deadline", expired, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "new-directory")
			st, err := Open(tc.ctx, Options{Path: filepath.Join(dir, "archive.db")})
			if st != nil {
				_ = st.Close()
				t.Fatal("open returned a store for a finished context")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("open error = %v, want %v", err, tc.want)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("finished-context open created a database directory: %v", err)
			}
		})
	}
}
