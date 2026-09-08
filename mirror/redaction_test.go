package mirror

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitErrorsRedactURLCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX Git executable")
	}
	dir := t.TempDir()
	git := filepath.Join(dir, "git-error")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >&2\nprintf 'fatal: rejected https://other:sample@example.invalid/repo\\n' >&2\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{
		"https://user:placeholder@example.invalid/repo",
		"https://test-auth-token@example.invalid/repo",
		(&url.URL{Scheme: "https", Host: "example.invalid", User: url.UserPassword("user", "fake/part"), Path: "/repo"}).String(),
		"https://user:placeholder@example.invalid/invalid%zz",
	} {
		err := run(context.Background(), dir, git, "fetch", remote)
		if err == nil {
			t.Fatal("expected Git failure")
		}
		for _, secret := range []string{"placeholder", "test-auth-token", "fake%2Fpart", "sample"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Git error leaked %q: %v", secret, err)
			}
		}
		if !strings.Contains(err.Error(), "example.invalid/") || !strings.Contains(err.Error(), "fatal: rejected") {
			t.Fatalf("useful error context lost: %v", err)
		}
	}
}

func TestSuccessfulGitOutputIsNotRedacted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX Git executable")
	}
	git := filepath.Join(t.TempDir(), "git-output")
	want := "https://user:example@example.invalid/repo"
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf '%s' '"+want+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := output(context.Background(), "", git, "show", "HEAD:document")
	if err != nil || got != want {
		t.Fatalf("archive bytes changed: %q, %v", got, err)
	}
}
