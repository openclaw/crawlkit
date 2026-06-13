package progress

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTrackerLogsCIStablePercentLines(t *testing.T) {
	var out bytes.Buffer
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	logger := testLogger(&out)

	tracker := New(logger, Options{
		Name:     "sync",
		Unit:     "channels",
		Total:    4,
		MinDelta: 2,
		Attrs:    []any{"source", "api"},
		Now:      func() time.Time { return now },
	})
	tracker.Add(1)
	tracker.Add(1, "phase", "messages")
	tracker.Finish(nil)

	logs := out.String()
	if strings.Count(logs, `msg="sync progress"`) != 3 {
		t.Fatalf("expected start, threshold, and finish lines:\n%s", logs)
	}
	for _, want := range []string{
		`state=started`,
		`state=progress`,
		`state=finished`,
		`unit=channels`,
		`done=2`,
		`total=4`,
		`percent=50.0`,
		`completion=50.0%`,
		`source=api`,
		`phase=messages`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("missing %q in logs:\n%s", want, logs)
		}
	}
}

func TestTrackerFinishReportsFailure(t *testing.T) {
	var out bytes.Buffer
	logger := testLogger(&out)
	tracker := New(logger, Options{Name: "import", Total: 10})

	tracker.Set(3)
	tracker.Finish(errors.New("boom"))

	logs := out.String()
	for _, want := range []string{`state=failed`, `done=3`, `percent=30.0`, `err=boom`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("missing %q in logs:\n%s", want, logs)
		}
	}
}

func TestTrackerAddIsAtomic(t *testing.T) {
	var out bytes.Buffer
	tracker := New(testLogger(&out), Options{Name: "sync", Total: 100, MinDelta: 1000})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracker.Add(1)
		}()
	}
	wg.Wait()
	if got := tracker.current(); got != 100 {
		t.Fatalf("done = %d, want 100", got)
	}
}

func TestCompletionClampsToRange(t *testing.T) {
	if got := Completion(-1, 4); got != "0.0%" {
		t.Fatalf("low clamp = %s", got)
	}
	if got := Completion(5, 4); got != "100.0%" {
		t.Fatalf("high clamp = %s", got)
	}
	if got := Completion(1, 0); got != "" {
		t.Fatalf("unknown total = %q", got)
	}
}

func testLogger(out *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))
}
