package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/crawlkit/scheduler"
)

func BenchmarkStatusHistory(b *testing.B) {
	for _, jobs := range []int{1, 20, 100} {
		b.Run(fmt.Sprintf("jobs=%d", jobs), func(b *testing.B) {
			configPath := filepath.Join(b.TempDir(), "crawlctl.toml")
			cfg := scheduler.DefaultConfig()
			for j := range jobs {
				cfg.Jobs[fmt.Sprintf("job-%03d", j)] = scheduler.Job{Enabled: true, Command: []string{"unused"}}
			}
			paths, err := scheduler.Save(configPath, cfg, false)
			if err != nil {
				b.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(paths.History), 0o700); err != nil {
				b.Fatal(err)
			}
			file, err := os.Create(paths.History)
			if err != nil {
				b.Fatal(err)
			}
			encoder := json.NewEncoder(file)
			for i := range 20000 {
				record := scheduler.RunRecord{Job: fmt.Sprintf("job-%03d", i%jobs), Repo: fmt.Sprintf("example/repo-%d", i%10), Status: "success", FinishedAt: "2026-05-22T10:00:00Z"}
				if err := encoder.Encode(record); err != nil {
					_ = file.Close()
					b.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				b.Fatal(err)
			}
			a := app{stdout: io.Discard, stderr: io.Discard, config: configPath}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := a.runStatus(nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
