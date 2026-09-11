package worker

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testQueue struct {
	db            *sql.DB
	path          string
	claimError    atomic.Bool
	completeError atomic.Bool
	retryError    atomic.Bool
	releaseError  atomic.Bool
	oversize      atomic.Bool
}

func queueAt(t *testing.T, path string) *testQueue {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`create table if not exists tasks(key text primary key, revision text, payload text, result text default '', state text default 'pending', class text, token text default '', until_ns integer default 0, ready_ns integer default 0, attempts integer default 0)`)
	if err != nil {
		t.Fatal(err)
	}
	return &testQueue{db: db, path: path}
}
func newQueue(t *testing.T) *testQueue { return queueAt(t, filepath.Join(t.TempDir(), "jobs.sqlite")) }
func (q *testQueue) put(t *testing.T, key, rev, payload string, class Class) {
	t.Helper()
	_, e := q.db.Exec(`insert into tasks(key,revision,payload,class)values(?,?,?,?) on conflict(key) do update set revision=excluded.revision,payload=excluded.payload,state='pending',token='',until_ns=0,ready_ns=0,attempts=0,class=excluded.class`, key, rev, payload, string(class))
	if e != nil {
		t.Fatal(e)
	}
}
func (q *testQueue) Claim(ctx context.Context, r ClaimRequest) ([]Job[string], error) {
	if q.claimError.Load() {
		return nil, errors.New("fixture")
	}
	tx, e := q.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `select key,revision,payload,attempts from tasks where state='pending' and class=? and ready_ns<=? and until_ns<=? order by key limit ?`, string(r.Class), r.Now.UnixNano(), r.Now.UnixNano(), r.Limit)
	if e != nil {
		return nil, e
	}
	var jobs []Job[string]
	for rows.Next() {
		var j Job[string]
		if e = rows.Scan(&j.Key, &j.Revision, &j.Payload, &j.Attempts); e != nil {
			rows.Close()
			return nil, e
		}
		j.Token = rand.Text()
		jobs = append(jobs, j)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, j := range jobs {
		if _, e = tx.ExecContext(ctx, `update tasks set token=?,until_ns=? where key=?`, j.Token, r.LeaseUntil.UnixNano(), j.Key); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	if q.oversize.Load() && len(jobs) > 0 {
		jobs = append(jobs, jobs[0])
	}
	return jobs, nil
}
func (q *testQueue) mutate(ctx context.Context, j Job[string], now time.Time, set string, args ...any) (bool, error) {
	args = append(args, j.Key, j.Revision, j.Token, now.UnixNano())
	r, e := q.db.ExecContext(ctx, `update tasks set `+set+` where key=? and revision=? and token=? and until_ns>? and state='pending'`, args...)
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	return n == 1, e
}
func (q *testQueue) Complete(ctx context.Context, j Job[string], result string, now time.Time) (bool, error) {
	if q.completeError.Load() {
		return false, errors.New("fixture")
	}
	return q.mutate(ctx, j, now, `result=?,state='done',token='',until_ns=0`, result)
}
func (q *testQueue) Retry(ctx context.Context, j Job[string], r Retry) (bool, error) {
	if q.retryError.Load() {
		return false, errors.New("fixture")
	}
	state := "pending"
	if r.Permanent {
		state = "failed"
	}
	inc := 0
	if r.CountAttempt {
		inc = 1
	}
	return q.mutate(ctx, j, r.Now, `state=?,ready_ns=?,attempts=attempts+?,token='',until_ns=0`, state, r.At.UnixNano(), inc)
}
func (q *testQueue) Release(ctx context.Context, j Job[string], now time.Time) (bool, error) {
	if q.releaseError.Load() {
		return false, errors.New("fixture")
	}
	return q.mutate(ctx, j, now, `token='',until_ns=0`)
}
func (q *testQueue) count(state string) int {
	var n int
	_ = q.db.QueryRow(`select count(*) from tasks where state=?`, state).Scan(&n)
	return n
}
func fastOpts() Options {
	return Options{Kind: "documents", Concurrency: 2, BatchSize: 4, BatchWait: time.Millisecond, PollEvery: 2 * time.Millisecond, TaskTimeout: time.Second, StoreTimeout: time.Second, RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond}
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func run(t *testing.T, q *testQueue, h Handler[string, string], opts Options) (*Runner[string, string], context.CancelFunc) {
	t.Helper()
	r, e := New[string, string](q, h, opts)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(3 * time.Second):
			t.Error("worker failed to stop")
		}
	})
	return r, cancel
}
func upper(_ context.Context, jobs []Job[string]) ([]string, error) {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = strings.ToUpper(j.Payload)
	}
	return out, nil
}

func TestDurableQueueRevisionDeleteAndReclaim(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	q.put(t, "message", "v1", "old", Fresh)
	now := time.Now()
	req := ClaimRequest{Kind: "documents", Class: Fresh, Limit: 1, Now: now, LeaseUntil: now.Add(time.Second)}
	jobs, e := q.Claim(ctx, req)
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	old := jobs[0]
	if e = ValidateJob(old); e != nil {
		t.Fatal(e)
	}
	q.put(t, "message", "v2", "new", Fresh)
	if ok, e := q.Complete(ctx, old, "STALE", now); ok || e != nil {
		t.Fatal("stale completion", ok, e)
	}
	if ok, e := q.Retry(ctx, old, Retry{Now: now, Permanent: true}); ok || e != nil {
		t.Fatal("stale retry", ok, e)
	}
	if ok, e := q.Release(ctx, old, now); ok || e != nil {
		t.Fatal("stale release", ok, e)
	}
	jobs, e = q.Claim(ctx, req)
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	crashed := jobs[0]
	reopened := queueAt(t, q.path)
	req.Now = now.Add(2 * time.Second)
	req.LeaseUntil = now.Add(3 * time.Second)
	jobs, e = reopened.Claim(ctx, req)
	if e != nil || len(jobs) != 1 || jobs[0].Token == crashed.Token {
		t.Fatal(jobs, e)
	}
	if ok, _ := q.Complete(ctx, crashed, "STALE", req.Now); ok {
		t.Fatal("expired owner committed")
	}
	if ok, e := q.Complete(ctx, jobs[0], "NEW", req.Now); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if ok, _ := q.Complete(ctx, jobs[0], "DUPLICATE", req.Now); ok {
		t.Fatal("duplicate completion")
	}
	q.put(t, "message", "v3", "deleted", Fresh)
	req.Now = time.Now()
	req.LeaseUntil = req.Now.Add(time.Second)
	jobs, e = q.Claim(ctx, req)
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	if _, e = q.db.Exec(`delete from tasks where key='message'`); e != nil {
		t.Fatal(e)
	}
	q.put(t, "message", "v3", "recreated", Fresh)
	if ok, _ := q.Complete(ctx, jobs[0], "DELETED", time.Now()); ok {
		t.Fatal("deleted lease resurrected result")
	}
}
func TestRunBatchesAndGitcrawlHashRevision(t *testing.T) {
	q := newQueue(t)
	for i := 0; i < 20; i++ {
		q.put(t, fmt.Sprint(i), "sha256:gitcrawl-title_original", fmt.Sprint(i), Fresh)
	}
	var peak atomic.Int64
	r, cancel := run(t, q, func(ctx context.Context, j []Job[string]) ([]string, error) {
		if len(j) > 4 {
			t.Error("unbounded batch")
		}
		peak.Add(1)
		defer peak.Add(-1)
		return upper(ctx, j)
	}, fastOpts())
	eventually(t, func() bool { return q.count("done") == 20 })
	cancel()
	eventually(t, func() bool { return r.Status().State == "stopped" })
	if r.Status().Completed != 20 || r.Status().LastSuccessAt.IsZero() {
		t.Fatal(r.Status())
	}
}
func TestFreshnessWhileCatchupAndSlowWork(t *testing.T) {
	q := newQueue(t)
	for i := 0; i < 100; i++ {
		q.put(t, fmt.Sprintf("old-%03d", i), "1", "old", CatchUp)
	}
	opts := fastOpts()
	opts.BatchSize = 1
	started := make(chan struct{}, 2)
	unblock := make(chan struct{})
	r, _ := run(t, q, func(ctx context.Context, j []Job[string]) ([]string, error) {
		if j[0].Payload == "old" {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-unblock:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return upper(ctx, j)
	}, opts)
	<-started
	before := time.Now()
	q.put(t, "fresh", "1", "new", Fresh)
	r.Notify()
	close(unblock)
	eventually(t, func() bool {
		var s string
		_ = q.db.QueryRow(`select state from tasks where key='fresh'`).Scan(&s)
		return s == "done"
	})
	if time.Since(before) > time.Second {
		t.Fatal("fresh input waited behind catch-up")
	}
	eventually(t, func() bool { return q.count("done") > 5 })
}
func TestRetryPauseAndPermanentFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure *Failure
		want    string
	}{{"retry", &Failure{Code: "temporary"}, "done"}, {"pause", &Failure{Code: "throttled", Pause: true, RetryAfter: 30 * time.Millisecond}, "done"}, {"permanent", &Failure{Code: "bad_input", Permanent: true}, "failed"}} {
		t.Run(test.name, func(t *testing.T) {
			q := newQueue(t)
			q.put(t, "a", "1", "a", Fresh)
			var calls atomic.Int32
			var first time.Time
			r, _ := run(t, q, func(ctx context.Context, j []Job[string]) ([]string, error) {
				if calls.Add(1) == 1 {
					first = time.Now()
					return nil, test.failure
				}
				if test.failure.Pause && time.Since(first) < test.failure.RetryAfter {
					t.Error("ignored retry-after")
				}
				return upper(ctx, j)
			}, fastOpts())
			eventually(t, func() bool { return q.count(test.want) == 1 })
			if test.want == "failed" {
				eventually(t, func() bool { return r.Status().Failed == 1 })
			} else {
				eventually(t, func() bool { return r.Status().Retried == 1 })
			}
		})
	}
}
func TestHandlerCancellationReleasesClaim(t *testing.T) {
	q := newQueue(t)
	q.put(t, "a", "1", "a", Fresh)
	entered := make(chan struct{})
	r, cancel := run(t, q, func(ctx context.Context, _ []Job[string]) ([]string, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}, fastOpts())
	<-entered
	if e := r.Run(context.Background()); e == nil {
		t.Fatal("allowed duplicate runner")
	}
	cancel()
	eventually(t, func() bool { return r.Status().State == "stopped" })
	var token string
	_ = q.db.QueryRow(`select token from tasks where key='a'`).Scan(&token)
	if token != "" || q.count("pending") != 1 {
		t.Fatal("claim not released")
	}
}
func TestInputEditDuringHandlerDiscardsOldResult(t *testing.T) {
	q := newQueue(t)
	q.put(t, "a", "1", "old", Fresh)
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var once atomic.Bool
	r, _ := run(t, q, func(ctx context.Context, j []Job[string]) ([]string, error) {
		if once.CompareAndSwap(false, true) {
			close(entered)
			<-proceed
		}
		return upper(ctx, j)
	}, fastOpts())
	<-entered
	q.put(t, "a", "2", "new", Fresh)
	r.Notify()
	close(proceed)
	eventually(t, func() bool { return q.count("done") == 1 && r.Status().Superseded == 1 })
	var result string
	_ = q.db.QueryRow(`select result from tasks where key='a'`).Scan(&result)
	if result != "NEW" {
		t.Fatal(result)
	}
}
func TestErrorsDoNotKillRunner(t *testing.T) {
	for _, mode := range []string{"claim", "complete", "retry", "release", "oversize", "panic", "count", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			q := newQueue(t)
			q.put(t, "a", "1", "a", Fresh)
			opts := fastOpts()
			opts.BatchSize = 1
			opts.MaxAttempts = 1
			switch mode {
			case "claim":
				q.claimError.Store(true)
			case "complete":
				q.completeError.Store(true)
			case "retry":
				q.retryError.Store(true)
			case "release":
				q.completeError.Store(true)
				q.releaseError.Store(true)
			case "oversize":
				q.oversize.Store(true)
			}
			r, cancel := run(t, q, func(ctx context.Context, j []Job[string]) ([]string, error) {
				switch mode {
				case "panic":
					panic("private body")
				case "count":
					return nil, nil
				case "retry":
					return nil, &Failure{Code: "fixture"}
				case "unknown":
					return nil, errors.New("secret body")
				}
				return upper(ctx, j)
			}, opts)
			eventually(t, func() bool { return r.Status().LastError != "" })
			if strings.Contains(r.Status().LastError, "body") {
				t.Fatal("leaked handler text")
			}
			cancel()
		})
	}
}
func TestOptionsAndBackoff(t *testing.T) {
	q := newQueue(t)
	if _, e := New[string, string](nil, upper, Options{Kind: "x"}); e == nil {
		t.Fatal("nil queue")
	}
	if _, e := New[string, string](q, nil, Options{Kind: "x"}); e == nil {
		t.Fatal("nil handler")
	}
	for _, opts := range []Options{{}, {Kind: "x", Concurrency: -1}, {Kind: "x", LeaseDuration: time.Nanosecond}, {Kind: "x", RetryBase: time.Second, RetryMax: time.Millisecond}} {
		if _, e := New[string, string](q, upper, opts); e == nil {
			t.Fatal(opts)
		}
	}
	r, e := New[string, string](q, upper, Options{Kind: "x"})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		r.Notify()
	}
	if r.backoff(100) != time.Minute {
		t.Fatal("uncapped backoff")
	}
	if e = ValidateJob(Job[string]{}); e == nil {
		t.Fatal("invalid claim")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = r.Run(ctx); e != nil {
		t.Fatal(e)
	}
}

func TestIndependentTaskKinds(t *testing.T) {
	blocked := newQueue(t)
	blocked.put(t, "message", "revision-2", "text", Fresh)
	available := newQueue(t)
	available.put(t, "issue", "sha256:gitcrawl-content", "title and body", Fresh)
	entered := make(chan struct{})
	_, stop := run(t, blocked, func(ctx context.Context, j []Job[string]) ([]string, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}, fastOpts())
	<-entered
	opts := fastOpts()
	opts.Kind = "document_hash"
	other, _ := run(t, available, upper, opts)
	eventually(t, func() bool { return available.count("done") == 1 })
	if other.Status().Kind != "document_hash" {
		t.Fatal(other.Status())
	}
	stop()
}
