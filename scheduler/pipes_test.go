package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSchedulerPipeHelperProcess(t *testing.T) {
	mode := os.Getenv("CRAWLKIT_TEST_PIPE_MODE")
	if mode == "" {
		return
	}
	release := os.Getenv("CRAWLKIT_TEST_PIPE_RELEASE")
	if mode == "hold" {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(release); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	child := exec.Command(exe, "-test.run=^TestSchedulerPipeHelperProcess$")
	child.Env = append(os.Environ(), "CRAWLKIT_TEST_PIPE_MODE=hold")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(release+".pid", []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		os.Exit(4)
	}
	fmt.Println(`{"id":"synthetic","display_name":"Synthetic"}`)
	os.Exit(0)
}

func TestSchedulerBoundsInheritedOutputPipes(t *testing.T) {
	for _, operation := range []string{"run", "discover"} {
		t.Run(operation, func(t *testing.T) {
			if operation == "discover" && runtime.GOOS == "windows" {
				t.Skip("POSIX executable wrapper")
			}
			dir := t.TempDir()
			release := filepath.Join(dir, "release")
			t.Setenv("CRAWLKIT_TEST_PIPE_MODE", "parent")
			t.Setenv("CRAWLKIT_TEST_PIPE_RELEASE", release)
			t.Cleanup(func() {
				_ = os.WriteFile(release, nil, 0o600)
				if data, err := os.ReadFile(release + ".pid"); err == nil {
					if pid, err := strconv.Atoi(string(data)); err == nil {
						if process, err := os.FindProcess(pid); err == nil {
							_ = process.Kill()
							_ = process.Release()
						}
					}
				}
			})
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan string, 1)
			if operation == "run" {
				go func() {
					record := runCommand(context.Background(), time.Now, dir, "synthetic", "", []string{exe, "-test.run=^TestSchedulerPipeHelperProcess$"}, "", nil, 1024)
					done <- record.Error
				}()
			} else {
				wrapper := filepath.Join(dir, "synthetic")
				script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run='^TestSchedulerPipeHelperProcess$'\n"
				if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				go func() { done <- discoverOne(context.Background(), wrapper).Error }()
			}
			select {
			case message := <-done:
				if _, err := os.Stat(release + ".pid"); err != nil {
					t.Fatalf("helper did not start a descendant: %v", err)
				}
				if !strings.Contains(message, exec.ErrWaitDelay.Error()) {
					t.Fatalf("expected output-drain error, got %q", message)
				}
			case <-time.After(8 * time.Second):
				_ = os.WriteFile(release, nil, 0o600)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("helper failed to unblock during cleanup")
				}
				t.Fatal("scheduler waited for a descendant after its command exited")
			}
		})
	}
}
