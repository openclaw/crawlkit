//go:build darwin

package sessionowner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fileFixture(t *testing.T) (*FileBackend, string) {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	b, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		t.Fatal(e)
	}
	return b, dir
}
func TestFileCASAndLostAcknowledgment(t *testing.T) {
	b, _ := fileFixture(t)
	ctx := context.Background()
	g, e := b.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	s, e := b.Load(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	expected := s.Expected()
	s.Generation = "epoch-1"
	s.Credentials = map[string]any{"token": "synthetic"}
	s.Claims["claim-1"] = &Claim{ID: "claim-1", Epoch: "epoch-1", Phase: "WAIT_READINESS", NoEffectProved: true}
	s.Fences["throttle"] = time.Now().Add(time.Hour)
	s.Journal["fixture"] = &JournalEntry{RequestID: "req-1", Certainty: "uncertain", ReceiptID: "receipt-1"}
	b.options.Fault = func(stage FileStage) error {
		if stage == AfterRename {
			return os.ErrClosed
		}
		return nil
	}
	r, e := b.Commit(ctx, g, expected, s, "commit-1")
	if e == nil || r.Certainty != "uncertain" {
		t.Fatal("fault not injected")
	}
	b.options.Fault = nil
	r, e = b.Reconcile(ctx, g, "commit-1")
	if e != nil || r.Certainty != "committed" {
		t.Fatal("readback")
	}
	loaded, e := b.Load(ctx, g)
	if e != nil || loaded.Claims["claim-1"] == nil || loaded.Journal["fixture"] == nil || loaded.Fences["throttle"].IsZero() {
		t.Fatal("atomic snapshot")
	}
	if _, e = b.Commit(ctx, g, expected, s, "commit-2"); e == nil {
		t.Fatal("stale CAS")
	}
}
func TestFileUnsafeStorageAndLockInode(t *testing.T) {
	b, dir := fileFixture(t)
	ctx := context.Background()
	g, e := b.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(filepath.Join(dir, "authority.lock"), filepath.Join(dir, "old-lock")); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "authority.lock"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Load(ctx, g); e == nil {
		t.Fatal("lock inode replacement")
	}
	g.Close()
	b, dir = fileFixture(t)
	target := filepath.Join(dir, "other")
	if e = os.WriteFile(target, []byte("synthetic"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(target, filepath.Join(dir, "authority.json")); e != nil {
		t.Fatal(e)
	}
	g, e = b.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Load(ctx, g); e == nil {
		t.Fatal("symlink")
	}
	g.Close()
	b, dir = fileFixture(t)
	if e = os.WriteFile(filepath.Join(dir, "authority.lock"), nil, 0644); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Join(dir, "authority.lock"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Acquire(ctx); e == nil {
		t.Fatal("unsafe mode")
	}
}
func childCommand(t *testing.T, mode, dir string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFileChildHelper$")
	cmd.Env = []string{"SESSIONOWNER_TEST_CHILD=" + mode, "SESSIONOWNER_TEST_DIR=" + dir, "TMPDIR=" + os.Getenv("TMPDIR")}
	return cmd
}
func TestFileCrossProcessContentionAndCrash(t *testing.T) {
	b, dir := fileFixture(t)
	g, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	out, e := childCommand(t, "busy", dir).CombinedOutput()
	if e != nil || !strings.Contains(string(out), "EXPECTED_BUSY") {
		t.Fatal("cross-process exclusion")
	}
	g.Close()
	for _, stage := range []FileStage{BeforeStage, AfterWrite, AfterSync, AfterRename, AfterDirectorySync} {
		t.Run(string(stage), func(t *testing.T) {
			b, dir := fileFixture(t)
			cmd := childCommand(t, string(stage), dir)
			if e := cmd.Run(); e == nil {
				t.Fatal("expected child crash")
			}
			g, e := b.Acquire(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer g.Close()
			r, e := b.Reconcile(context.Background(), g, "child-commit")
			if e != nil {
				t.Fatal(e)
			}
			want := "not_committed"
			if stage == AfterRename || stage == AfterDirectorySync {
				want = "committed"
			}
			if r.Certainty != want {
				t.Fatalf("stage %s: %s", stage, r.Certainty)
			}
		})
	}
}
func TestFileChildHelper(t *testing.T) {
	mode := os.Getenv("SESSIONOWNER_TEST_CHILD")
	if mode == "" {
		return
	}
	dir := os.Getenv("SESSIONOWNER_TEST_DIR")
	b, e := NewFileBackend(FileOptions{Directory: dir, BindingID: "fixture-binding"})
	if e != nil {
		os.Exit(71)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	g, e := b.Acquire(ctx)
	if mode == "busy" {
		if e == nil {
			g.Close()
			os.Exit(72)
		}
		os.Stdout.WriteString("EXPECTED_BUSY\n")
		return
	}
	if e != nil {
		os.Exit(73)
	}
	defer g.Close()
	s, e := b.Load(ctx, g)
	if e != nil {
		os.Exit(74)
	}
	expected := s.Expected()
	s.Generation = "epoch-1"
	b.options.Fault = func(stage FileStage) error {
		if string(stage) == mode {
			os.Exit(67)
		}
		return nil
	}
	b.Commit(ctx, g, expected, s, "child-commit")
	os.Exit(75)
}

func TestOwnedFD3ChildDeathBeforeAndAfterACK(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode, func(t *testing.T) {
			reached, reaped := false, false
			originalReceipt := ""
			originalCommits := map[string]CommitReceipt{}
			o, backend, channel := fixtureOwner(t, func(ctx context.Context, inv *Invocation) (AdapterOutput, error) {
				fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
				if err != nil {
					return AdapterOutput{}, err
				}
				parent := os.NewFile(uintptr(fds[0]), "owned-parent-fd")
				child := os.NewFile(uintptr(fds[1]), "owned-child-fd")
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFD3ChildHelper$")
				cmd.Env = []string{"SESSIONOWNER_FD_CHILD=" + mode, "TMPDIR=" + os.Getenv("TMPDIR")}
				cmd.ExtraFiles = []*os.File{child}
				if err = cmd.Start(); err != nil {
					parent.Close()
					child.Close()
					return AdapterOutput{}, err
				}
				child.Close()
				session, err := NewAdapterSession(inv, "owned-child-lifetime")
				if err != nil {
					parent.Close()
					cmd.Process.Kill()
					cmd.Wait()
					return AdapterOutput{}, err
				}
				result, runErr := RunPrivateFD(ctx, session, parent)
				waitErr := cmd.Wait() // owned child is always reaped, never left as a service.
				reaped = cmd.ProcessState != nil
				wantExit, wantDispatches, wantPermitted := 66, 0, false
				wantLoss := "not_dispatched"
				if mode == "after" {
					wantExit, wantDispatches, wantPermitted = 67, 1, true
					wantLoss = "uncertain"
				}
				if waitErr == nil || !reaped || !cmd.ProcessState.Exited() || cmd.ProcessState.ExitCode() != wantExit || ctx.Err() != nil {
					t.Fatalf("owned child missed intended abrupt exit: mode=%s state=%v wait=%v context=%v", mode, cmd.ProcessState, waitErr, ctx.Err())
				}
				j := inv.snapshot.Journal[inv.journalKey]
				if runErr == nil || session.permitted.Load() != wantPermitted || session.ParentLost() != wantLoss || j.Dispatches != wantDispatches || len(j.EffectIDs) != wantDispatches || len(j.Intents) != wantDispatches {
					t.Fatal("child loss crossed the wrong durable ACK/permit boundary", mode)
				}
				reached = true
				originalReceipt = j.ReceiptID
				for id, receipt := range inv.snapshot.Commits {
					originalCommits[id] = receipt
				}
				return result, runErr
			})
			request := fixtureRequest("perform")
			request["operationId"] = "fixture.write"
			request["confirmationRef"] = "approval-1"
			result := resultMap(t)(o.Handle(context.Background(), channel, rawJSON(t, request)))
			want := "not_dispatched"
			if mode == "after" {
				want = "uncertain"
			}
			if result["certainty"] != want {
				t.Fatalf("%s child: %v", mode, result["certainty"])
			}
			if !reached || !reaped || result["kind"] != "failed" || result["requestId"] != request["requestId"] || result["receiptRef"] != originalReceipt {
				t.Fatal("child stage, reaping or original correlation missing")
			}
			guard, err := backend.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := backend.Load(context.Background(), guard)
			guard.Close()
			if err != nil {
				t.Fatal(err)
			}
			j := snapshot.Journal["alice\x00"+text(request, "requestId")]
			wantDispatches := 0
			if mode == "after" {
				wantDispatches = 1
			}
			if len(snapshot.Journal) != 1 || j == nil || j.ReceiptID != originalReceipt || j.Certainty != want || j.Dispatches != wantDispatches || len(j.EffectIDs) != wantDispatches || len(j.Intents) != wantDispatches || j.UnresolvedIntentID != "" {
				t.Fatal("child loss changed original history or permit accounting")
			}
			for id, receipt := range originalCommits {
				if !receiptMatches(snapshot.Commits[id], receipt) {
					t.Fatal("child loss replaced an original commit receipt")
				}
			}
		})
	}
}

func TestFD3ChildHelper(t *testing.T) {
	mode := os.Getenv("SESSIONOWNER_FD_CHILD")
	if mode == "" {
		return
	}
	fd := os.NewFile(3, "inherited-private-channel")
	defer fd.Close()
	raw, err := ReadFrame(fd, HardFrameLimit)
	if err != nil {
		os.Exit(81)
	}
	v, err := DecodeJSON(raw, HardFrameLimit)
	if err != nil {
		os.Exit(82)
	}
	invoke, _ := object(v)
	if mode == "before" {
		// This fixture models abrupt child loss, not successful process shutdown.
		// Race-instrumented Exit(0) runs racefini before closing inherited FDs,
		// which was observed to turn this boundary into a one-second deadline kill.
		// Nonzero exit keeps the real child/FD semantics without changing timers.
		os.Exit(66)
	}
	checkpoint := map[string]any{"protocol": AdapterProtocol, "kind": "checkpoint", "direction": "adapter_to_authority", "lease": invoke["lease"], "sequence": "0", "phase": "EXECUTING", "event": "before_effect", "effectCertainty": "not_dispatched", "evidenceId": "child-evidence", "evidence": map[string]any{"stage": "synthetic-before-effect"}}
	if WriteFrame(fd, rawJSON(t, checkpoint), HardFrameLimit) != nil {
		os.Exit(83)
	}
	ack, err := ReadFrame(fd, HardFrameLimit)
	if err != nil {
		os.Exit(84)
	}
	v, err = DecodeJSON(ack, HardFrameLimit)
	if err != nil {
		os.Exit(85)
	}
	m, _ := object(v)
	if m["authorizedToProceed"] != true {
		os.Exit(86)
	}
	os.Exit(67) // effect may have occurred; no terminal result is returned.
}
