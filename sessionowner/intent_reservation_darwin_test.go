//go:build darwin

package sessionowner

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

type a4CrashBackend struct {
	Backend
	stage string
}

func (b *a4CrashBackend) Commit(ctx context.Context, g *Guard, e Expected, s *Snapshot, id string) (CommitReceipt, error) {
	current, err := b.Backend.Load(ctx, g)
	if err != nil {
		return CommitReceipt{}, err
	}
	stage := a4TransactionStage(current, s, id)
	r, err := b.Backend.Commit(ctx, g, e, s, id)
	if err == nil && stage == b.stage {
		os.Exit(67)
	}
	return r, err
}

type a4ReadbackCounter struct {
	Backend
	id    string
	reads int
}

func (b *a4ReadbackCounter) Reconcile(ctx context.Context, g *Guard, id string) (CommitReceipt, error) {
	if id == b.id {
		b.reads++
	}
	return b.Backend.Reconcile(ctx, g, id)
}
func TestA4PrivateFileCrashBeforeAndAfterIntent(t *testing.T) {
	for _, stage := range []string{"reservation", "intent"} {
		t.Run(stage, func(t *testing.T) {
			file, dir := fileFixture(t)
			g, _ := file.Acquire(context.Background())
			empty, _ := file.Load(context.Background(), g)
			s := NewSnapshot("fixture-binding")
			s.Generation = "epoch-1"
			s.Credentials = map[string]any{"token": "synthetic-private-token"}
			s.Identity = map[string]any{"account": "expected"}
			if _, e := file.Commit(context.Background(), g, empty.Expected(), s, "seed-enrollment"); e != nil {
				t.Fatal(e)
			}
			g.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestA4CrashHelper$")
			cmd.Env = []string{"A4_CRASH_STAGE=" + stage, "A4_TEST_DIR=" + dir, "TMPDIR=" + os.Getenv("TMPDIR")}
			if e := cmd.Run(); e == nil {
				t.Fatal("expected crash checkpoint")
			}
			fresh, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
			if e != nil {
				t.Fatal(e)
			}
			g, _ = fresh.Acquire(context.Background())
			s, e = fresh.Load(context.Background(), g)
			g.Close()
			if e != nil {
				t.Fatal(e)
			}
			var claim *Claim
			var intent IntentReservation
			for _, c := range s.Claims {
				claim = c
			}
			for _, j := range s.Journal {
				for _, r := range j.Intents {
					intent = r
				}
			}
			if claim == nil || intent.ID == "" {
				t.Fatal("preparation not durable before child exit")
			}
			want := 0
			if stage == "intent" {
				want = 1
			}
			if claim.AcquisitionAttempts != want {
				t.Fatal("wrong crash counter")
			}
			if stage == "reservation" && intent.State != "prepared" || stage == "intent" && intent.State != "committed" {
				t.Fatal("wrong semantic crash stage")
			}
			o, _, _, effects, proofs := a4Owner(t)
			counter := &a4ReadbackCounter{Backend: fresh, id: intent.ID}
			o.backend = counter
			ch, _ := o.AuthenticateChannel(context.Background(), "alice")
			got := resultMap(t)(o.Handle(context.Background(), ch, rawJSON(t, b2Request("resume", claim.ID))))
			if counter.reads == 0 {
				t.Fatal("restart skipped original intent transaction")
			}
			if stage == "reservation" {
				if got["kind"] != "recovered" || *effects != 1 || *proofs != 1 {
					t.Fatal("never-attempted continuation refused")
				}
			} else {
				if got["kind"] != "failed" || *effects != 0 || a4Claim(t, fresh).AcquisitionAttempts != 1 {
					t.Fatal("committed witness refunded/replayed")
				}
			}
		})
	}
}
func TestA4CrashHelper(t *testing.T) {
	stage := os.Getenv("A4_CRASH_STAGE")
	if stage == "" {
		return
	}
	file, e := NewFileBackend(FileOptions{Directory: os.Getenv("A4_TEST_DIR"), BindingID: "fixture-binding"})
	if e != nil {
		os.Exit(81)
	}
	o, _, c, _, _ := a4Owner(t)
	o.backend = &a4CrashBackend{Backend: file, stage: stage}
	o.Handle(context.Background(), c, rawJSON(t, b2Request("new", "")))
	os.Exit(82)
}
