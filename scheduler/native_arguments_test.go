package scheduler

import (
	"strings"
	"testing"
)

func TestSystemdPlanPreservesLiteralArguments(t *testing.T) {
	plan, err := PlanInstall(InstallOptions{
		Backend: "systemd", Every: "1m", Executable: "/tmp/bin space-%n-${USER}/crawlctl",
		Paths: Paths{ConfigPath: "/tmp/literal-%n-${USER}-'quoted'/config.toml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `ExecStart=@"/tmp/bin space-%%n-${USER}/crawlctl" "/tmp/bin space-%%n-$${USER}/crawlctl" "--config" "/tmp/literal-%%n-$${USER}-'quoted'/config.toml" "run"`
	if !strings.Contains(plan.Content, want) {
		t.Fatalf("systemd command must preserve literal arguments:\n%s", plan.Content)
	}
}

func TestSystemdInstallRejectsUnsupportedExecutablePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	for _, character := range []string{"'", "\"", "\\", "\n", "\t", "\x00", "\x7f"} {
		_, err := Install(InstallOptions{Backend: "systemd", Every: "1m", Executable: "/tmp/bin" + character + "/crawlctl", Paths: Paths{ConfigPath: "/tmp/config.toml"}})
		if err == nil || !strings.Contains(err.Error(), "systemd executable path") {
			t.Fatalf("executable with %q: error = %v", character, err)
		}
	}
}

func TestSystemdPreviewPreservesForeignExecutablePaths(t *testing.T) {
	opts := InstallOptions{Backend: "systemd", Every: "1m", Executable: `C:\Program Files\crawlctl.exe`, Paths: Paths{ConfigPath: "/tmp/config.toml"}}
	if _, err := PlanInstall(opts); err != nil {
		t.Fatalf("portable plan: %v", err)
	}
	opts.DryRun = true
	if _, err := Install(opts); err != nil {
		t.Fatalf("portable preview: %v", err)
	}
}

func TestSystemdPlanEscapesControlCharacters(t *testing.T) {
	plan, err := PlanInstall(InstallOptions{
		Backend: "systemd", Every: "1m", Executable: "/tmp/bin/crawlctl",
		Paths: Paths{ConfigPath: "/tmp/tab\tline\nquote\"back\\slash/config.toml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `"/tmp/tab\tline\nquote\"back\\slash/config.toml"`
	if !strings.Contains(plan.Content, want) {
		t.Fatalf("systemd command must escape control characters:\n%s", plan.Content)
	}
}

func TestSystemdInstallPlanAllowsLiteralGlobCharacters(t *testing.T) {
	plan, err := planInstall(InstallOptions{Backend: "systemd", Every: "1m", Executable: "/tmp/bin[*?]/crawlctl", Paths: Paths{ConfigPath: "/tmp/config.toml"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Content, "bin[*?]/crawlctl") {
		t.Fatalf("literal executable was changed: %s", plan.Content)
	}
}
