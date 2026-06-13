// Package progress provides CI-safe progress logging for crawler jobs.
package progress

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const defaultLogEvery = 30 * time.Second

type Options struct {
	Name     string
	Unit     string
	Total    int64
	LogEvery time.Duration
	MinDelta int64
	Attrs    []any
	Now      func() time.Time
}

type Tracker struct {
	logger   *slog.Logger
	name     string
	unit     string
	total    int64
	logEvery time.Duration
	minDelta int64
	attrs    []any
	now      func() time.Time

	mu       sync.Mutex
	started  time.Time
	lastLog  time.Time
	lastDone int64
	done     int64
}

func New(logger *slog.Logger, opts Options) *Tracker {
	if logger == nil {
		return nil
	}
	opts = normalizeOptions(opts)
	now := opts.Now()
	t := &Tracker{
		logger:   logger,
		name:     opts.Name,
		unit:     opts.Unit,
		total:    opts.Total,
		logEvery: opts.LogEvery,
		minDelta: opts.MinDelta,
		attrs:    append([]any(nil), opts.Attrs...),
		now:      opts.Now,
		started:  now,
		lastLog:  now,
	}
	t.log("started", now, 0, nil)
	return t
}

func (t *Tracker) Add(delta int64, attrs ...any) {
	if t == nil || delta == 0 {
		return
	}
	now := t.now()
	t.mu.Lock()
	done := clampDone(t.done+delta, t.total)
	t.done = done
	shouldLog := t.shouldLogLocked(done, now)
	if shouldLog {
		t.lastDone = done
		t.lastLog = now
	}
	t.mu.Unlock()
	if shouldLog {
		t.log("progress", now, done, attrs)
	}
}

func (t *Tracker) Set(done int64, attrs ...any) {
	if t == nil {
		return
	}
	now := t.now()
	t.mu.Lock()
	done = clampDone(done, t.total)
	t.done = done
	shouldLog := t.shouldLogLocked(done, now)
	if shouldLog {
		t.lastDone = done
		t.lastLog = now
	}
	t.mu.Unlock()
	if shouldLog {
		t.log("progress", now, done, attrs)
	}
}

func (t *Tracker) Finish(err error, attrs ...any) {
	if t == nil {
		return
	}
	now := t.now()
	done := t.current()
	if t.total > 0 && err == nil {
		done = t.total
	}
	state := "finished"
	if err != nil {
		state = "failed"
		attrs = append(attrs, "err", err)
	}
	t.log(state, now, done, attrs)
}

func (t *Tracker) current() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

func (t *Tracker) shouldLogLocked(done int64, now time.Time) bool {
	return done == t.total ||
		done == 0 ||
		done-t.lastDone >= t.minDelta ||
		now.Sub(t.lastLog) >= t.logEvery
}

func clampDone(done, total int64) int64 {
	if done < 0 {
		return 0
	}
	if total > 0 && done > total {
		return total
	}
	return done
}

func (t *Tracker) log(state string, now time.Time, done int64, attrs []any) {
	all := []any{
		"name", t.name,
		"state", state,
		"done", done,
		"elapsed", now.Sub(t.started).Round(time.Second).String(),
	}
	if t.unit != "" {
		all = append(all, "unit", t.unit)
	}
	if t.total > 0 {
		all = append(all,
			"total", t.total,
			"remaining", max(t.total-done, 0),
			"percent", Percent(done, t.total),
			"completion", Completion(done, t.total),
		)
	}
	all = append(all, t.attrs...)
	all = append(all, attrs...)
	t.logger.Info(t.name+" progress", all...)
}

func Percent(done, total int64) string {
	if total <= 0 {
		return ""
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	return fmt.Sprintf("%.1f", float64(done)*100/float64(total))
}

func Completion(done, total int64) string {
	if total <= 0 {
		return ""
	}
	return Percent(done, total) + "%"
}

func normalizeOptions(opts Options) Options {
	if opts.Name == "" {
		opts.Name = "job"
	}
	if opts.LogEvery <= 0 {
		opts.LogEvery = defaultLogEvery
	}
	if opts.MinDelta <= 0 {
		opts.MinDelta = 1
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts
}
