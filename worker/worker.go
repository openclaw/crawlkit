// Package worker runs bounded background work against application-owned durable
// queues. It does not own source schemas, content selection, or result storage.
package worker

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// Class separates fresh input from catch-up work. A queue may implement both
// classes in one table; classification must survive process restarts.
type Class string

const (
	Fresh   Class = "fresh"
	CatchUp Class = "catch_up"
)

// Job is an immutable claimed input. Token must be unique per claim, including
// after a delete/recreate of Key. Revision is opaque (a generation or input hash).
type Job[P any] struct {
	Key      string
	Revision string
	Token    string
	Attempts int
	Payload  P
}

type ClaimRequest struct {
	Kind       string
	Class      Class
	Limit      int
	Now        time.Time
	LeaseUntil time.Time
}

type Retry struct {
	Now          time.Time
	At           time.Time
	Code         string
	Permanent    bool
	CountAttempt bool
}

// Queue implementations must atomically claim jobs and fence EVERY mutation by
// key, revision, token and an unexpired lease. Complete must verify current source
// eligibility/revision and commit the result and job completion in one transaction.
// False means superseded/lost lease, not a failure. Enqueue belongs in the source
// transaction; clearing a lease on a new revision invalidates in-flight work.
// Implementations must be safe for concurrent calls and honor context cancellation.
type Queue[P, R any] interface {
	Claim(context.Context, ClaimRequest) ([]Job[P], error)
	Complete(context.Context, Job[P], R, time.Time) (bool, error)
	Retry(context.Context, Job[P], Retry) (bool, error)
	Release(context.Context, Job[P], time.Time) (bool, error)
}

// Handler processes a bounded batch with no database transaction held. Results
// correspond positionally to jobs. External side effects need their own stable
// idempotency keys: execution is at least once, not exactly once.
type Handler[P, R any] func(context.Context, []Job[P]) ([]R, error)

// Failure classifies safe, content-free error codes. Pause describes an unavailable
// dependency (credentials, provider outage or throttling): pause this runner and
// reschedule without consuming per-job attempts. RetryAfter is a minimum delay.
type Failure struct {
	Code       string
	RetryAfter time.Duration
	Permanent  bool
	Pause      bool
}

func (e *Failure) Error() string { return e.Code }

type Options struct {
	Kind          string
	Concurrency   int
	BatchSize     int
	BatchWait     time.Duration
	PollEvery     time.Duration
	TaskTimeout   time.Duration
	StoreTimeout  time.Duration
	LeaseDuration time.Duration
	RetryBase     time.Duration
	RetryMax      time.Duration
	MaxAttempts   int
}

// Status contains no payloads or provider error bodies. Apps may persist snapshots
// and combine them with indexed queue counts/ages in their existing status surface.
type Status struct {
	Kind          string    `json:"kind"`
	State         string    `json:"state"`
	InFlight      int       `json:"in_flight"`
	Completed     int64     `json:"completed"`
	Superseded    int64     `json:"superseded"`
	Retried       int64     `json:"retried"`
	Failed        int64     `json:"failed"`
	LastSuccessAt time.Time `json:"last_success_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
	RetryAt       time.Time `json:"retry_at,omitzero"`
}

type Runner[P, R any] struct {
	queue   Queue[P, R]
	handler Handler[P, R]
	opts    Options
	wake    chan struct{}
	mu      sync.Mutex
	running bool
	claims  uint64
	status  Status
}

func New[P, R any](queue Queue[P, R], handler Handler[P, R], opts Options) (*Runner[P, R], error) {
	if queue == nil || handler == nil || strings.TrimSpace(opts.Kind) == "" {
		return nil, errors.New("worker requires a queue, handler and kind")
	}
	if opts.Concurrency < 0 || opts.BatchSize < 0 || opts.BatchWait < 0 || opts.PollEvery < 0 || opts.TaskTimeout < 0 || opts.StoreTimeout < 0 || opts.LeaseDuration < 0 || opts.RetryBase < 0 || opts.RetryMax < 0 || opts.MaxAttempts < 0 {
		return nil, errors.New("worker limits must not be negative")
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = 2
	}
	if opts.BatchSize == 0 {
		opts.BatchSize = 64
	}
	if opts.BatchWait == 0 {
		opts.BatchWait = 250 * time.Millisecond
	}
	if opts.PollEvery == 0 {
		opts.PollEvery = time.Second
	}
	if opts.TaskTimeout == 0 {
		opts.TaskTimeout = 2 * time.Minute
	}
	if opts.StoreTimeout == 0 {
		opts.StoreTimeout = 5 * time.Second
	}
	// Every result may need both a bounded store operation and failure cleanup. No heartbeat
	// is necessary: the lease covers processing plus the entire completion budget.
	availableOperations := (time.Duration(1<<63-1) - opts.TaskTimeout) / opts.StoreTimeout
	if availableOperations < 2 || uint64(opts.BatchSize) > uint64((availableOperations-2)/2) {
		return nil, errors.New("worker lease budget overflows duration")
	}
	minimumLease := opts.TaskTimeout + (2*time.Duration(opts.BatchSize)+2)*opts.StoreTimeout
	if opts.LeaseDuration == 0 {
		opts.LeaseDuration = minimumLease
	}
	if opts.LeaseDuration < minimumLease {
		return nil, errors.New("worker lease is shorter than its processing and completion budget")
	}
	if opts.RetryBase == 0 {
		opts.RetryBase = time.Second
	}
	if opts.RetryMax == 0 {
		opts.RetryMax = time.Minute
	}
	if opts.RetryMax < opts.RetryBase {
		return nil, errors.New("worker retry maximum is below base")
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 3
	}
	return &Runner[P, R]{queue: queue, handler: handler, opts: opts, wake: make(chan struct{}, 1), status: Status{Kind: opts.Kind, State: "stopped"}}, nil
}

// Notify is a coalesced wake-up hint; durable polling recovers lost notifications.
// It never blocks an ingestion transaction. Call it after that transaction commits.
func (r *Runner[P, R]) Notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *Runner[P, R]) Status() Status { r.mu.Lock(); defer r.mu.Unlock(); return r.status }

// Run starts this kind's worker pool. Multiple kinds use independent runners,
// avoiding a slow handler's head-of-line blocking of unrelated task kinds.
func (r *Runner[P, R]) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("worker is already running")
	}
	r.running = true
	r.status.State = "running"
	r.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < r.opts.Concurrency; i++ {
		wg.Go(func() { r.loop(ctx) })
	}
	wg.Wait()
	r.mu.Lock()
	r.running = false
	r.status.State = "stopped"
	r.mu.Unlock()
	return nil
}

func (r *Runner[P, R]) loop(ctx context.Context) {
	for ctx.Err() == nil {
		s := r.Status()
		if delay := time.Until(s.RetryAt); delay > 0 {
			if !r.wait(ctx, delay, false) {
				return
			}
		}
		if !r.wait(ctx, r.opts.BatchWait, false) {
			return
		}
		if time.Now().Before(r.Status().RetryAt) {
			continue
		}
		r.mu.Lock()
		r.claims++
		class := Fresh
		if r.claims%5 == 0 {
			class = CatchUp
		}
		r.mu.Unlock()
		jobs, err := r.claim(ctx, class)
		if err == nil && len(jobs) == 0 {
			if class == Fresh {
				class = CatchUp
			} else {
				class = Fresh
			}
			jobs, err = r.claim(ctx, class)
		}
		if err != nil {
			r.problem("queue_claim_failed")
			if !r.wait(ctx, r.opts.PollEvery, false) {
				return
			}
			continue
		}
		if len(jobs) == 0 {
			if !r.wait(ctx, r.opts.PollEvery, true) {
				return
			}
			continue
		}
		if len(jobs) > r.opts.BatchSize { // A broken adapter must not bypass the bound.
			r.problem("queue_batch_limit_exceeded")
			r.releaseBatch(jobs)
			if !r.wait(ctx, r.opts.PollEvery, false) {
				return
			}
			continue
		}
		valid := true
		for _, job := range jobs {
			if ValidateJob(job) != nil {
				valid = false
			}
		}
		if !valid {
			r.problem("queue_invalid_claim")
			r.releaseBatch(jobs)
			if !r.wait(ctx, r.opts.PollEvery, false) {
				return
			}
			continue
		}
		r.process(ctx, jobs)
	}
}

func (r *Runner[P, R]) claim(ctx context.Context, class Class) ([]Job[P], error) {
	call, cancel := context.WithTimeout(ctx, r.opts.StoreTimeout)
	defer cancel()
	now := time.Now().UTC()
	return r.queue.Claim(call, ClaimRequest{Kind: r.opts.Kind, Class: class, Limit: r.opts.BatchSize, Now: now, LeaseUntil: now.Add(r.opts.LeaseDuration)})
}

func (r *Runner[P, R]) process(ctx context.Context, jobs []Job[P]) {
	r.mu.Lock()
	r.status.InFlight += len(jobs)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.status.InFlight -= len(jobs); r.mu.Unlock() }()
	call, cancel := context.WithTimeout(ctx, r.opts.TaskTimeout)
	results, err := invoke(call, r.handler, jobs)
	if err == nil {
		err = call.Err()
	}
	cancel()
	if ctx.Err() != nil {
		r.releaseBatch(jobs)
		return
	}
	if err == nil && len(results) != len(jobs) {
		err = &Failure{Code: "handler_result_count", Permanent: true}
	}
	if err != nil {
		r.fail(ctx, jobs, err)
		return
	}
	for i, job := range jobs {
		if ctx.Err() != nil {
			r.releaseBatch(jobs[i:])
			return
		}
		call, cancel := context.WithTimeout(ctx, r.opts.StoreTimeout)
		accepted, err := r.queue.Complete(call, job, results[i], time.Now().UTC())
		cancel()
		if err != nil {
			r.problem("queue_complete_failed")
			if ctx.Err() != nil {
				r.releaseBatch(jobs[i:])
				return
			}
			r.release(job)
			continue
		}
		r.mu.Lock()
		if accepted {
			r.status.Completed++
			r.status.LastSuccessAt = time.Now().UTC()
			r.status.LastError = ""
			if !time.Now().Before(r.status.RetryAt) {
				r.status.State = "running"
			}
		} else {
			r.status.Superseded++
		}
		r.mu.Unlock()
	}
}

func invoke[P, R any](ctx context.Context, handler Handler[P, R], jobs []Job[P]) (results []R, err error) {
	defer func() {
		if recover() != nil {
			results = nil
			err = &Failure{Code: "handler_panic", Permanent: true}
		}
	}()
	return handler(ctx, jobs)
}

func (r *Runner[P, R]) fail(ctx context.Context, jobs []Job[P], cause error) {
	failure := &Failure{Code: "handler_failed"}
	var classified *Failure
	if errors.As(cause, &classified) {
		failure = classified
	}
	code := failure.Code
	if strings.TrimSpace(code) == "" {
		code = "handler_failed"
	}
	r.problem(code)
	for i, job := range jobs {
		if ctx.Err() != nil {
			r.releaseBatch(jobs[i:])
			return
		}
		delay := r.backoff(job.Attempts)
		if failure.RetryAfter > delay {
			delay = failure.RetryAfter
		}
		now := time.Now().UTC()
		at := now.Add(delay)
		permanent := failure.Permanent || (!failure.Pause && job.Attempts+1 >= r.opts.MaxAttempts)
		if failure.Pause {
			r.mu.Lock()
			if at.After(r.status.RetryAt) {
				r.status.RetryAt = at
			}
			r.status.State = "paused"
			r.mu.Unlock()
		}
		call, cancel := context.WithTimeout(ctx, r.opts.StoreTimeout)
		accepted, err := r.queue.Retry(call, job, Retry{Now: now, At: at, Code: code, Permanent: permanent, CountAttempt: !failure.Pause})
		cancel()
		if err != nil {
			r.problem("queue_retry_failed")
			if ctx.Err() != nil {
				r.releaseBatch(jobs[i:])
				return
			}
			r.release(job)
			continue
		}
		r.mu.Lock()
		if !accepted {
			r.status.Superseded++
		} else if permanent {
			r.status.Failed++
		} else {
			r.status.Retried++
		}
		r.mu.Unlock()
	}
}

func (r *Runner[P, R]) backoff(attempt int) time.Duration {
	delay := r.opts.RetryBase
	for i := 0; i < attempt && delay < r.opts.RetryMax; i++ {
		if delay > r.opts.RetryMax/2 {
			delay = r.opts.RetryMax
		} else {
			delay *= 2
		}
	}
	// Bounded additive jitter; an explicit provider retry delay is never shortened.
	delay += time.Duration(float64(delay) * 0.2 * rand.Float64())
	if delay > r.opts.RetryMax {
		delay = r.opts.RetryMax
	}
	return delay
}

// Cancellation gets one bounded cleanup budget for the batch. If storage is
// unavailable, remaining claims are recovered by lease expiry rather than
// delaying process shutdown by one timeout per job.
func (r *Runner[P, R]) releaseBatch(jobs []Job[P]) {
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.StoreTimeout)
	defer cancel()
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		if _, err := r.queue.Release(ctx, job, time.Now().UTC()); err != nil {
			r.problem("queue_release_failed")
		}
	}
}

func (r *Runner[P, R]) release(job Job[P]) {
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.StoreTimeout)
	defer cancel()
	if _, err := r.queue.Release(ctx, job, time.Now().UTC()); err != nil {
		r.problem("queue_release_failed")
	}
}
func (r *Runner[P, R]) problem(code string) {
	r.mu.Lock()
	r.status.LastError = code
	r.status.State = "retrying"
	r.mu.Unlock()
}
func (r *Runner[P, R]) wait(ctx context.Context, d time.Duration, wake bool) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	var hint <-chan struct{}
	if wake {
		hint = r.wake
	}
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	case <-hint:
		return ctx.Err() == nil
	}
}

// ValidateJob is available to adapters and contract tests. Tokens and revisions
// are mandatory even for a task with only one current worker.
func ValidateJob[P any](job Job[P]) error {
	if job.Key == "" || job.Revision == "" || job.Token == "" || job.Attempts < 0 {
		return fmt.Errorf("invalid worker claim identity")
	}
	return nil
}
