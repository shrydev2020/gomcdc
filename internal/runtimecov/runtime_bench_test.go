package runtimecov

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func BenchmarkCollectDetailed(b *testing.B) {
	dataDir := b.TempDir()
	path := filepath.Join(dataDir, eventFilePrefix+"benchmark"+eventFileSuffix)
	file, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for index := 1; index <= 10000; index++ {
		if err := encoder.Encode(wireRecord{
			Type: "evaluation", RunID: "benchmark", PackagePath: "example.test/p", ProcessID: 1,
			EvaluationID: uint64(index), DecisionID: uint64(index), ConditionCount: 2,
			Conditions: []uint8{1, 2}, Result: true, Status: 0,
		}); err != nil {
			_ = file.Close()
			b.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := CollectDetailed(context.Background(), dataDir); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInjectedRuntimeWriter(b *testing.B) {
	cases := []struct {
		name        string
		body        string
		evaluations int
	}{
		{
			name: "duplicate-heavy",
			body: `
	for iteration := 0; iteration < 10000; iteration++ {
		var slot uint64
		hooks.BeginInto(&slot, 7, 1)
		hooks.Condition(slot, 0, true)
		hooks.End(slot, true)
	}`,
			evaluations: 10_000,
		},
		{
			name: "unique-heavy",
			body: `
	for vector := 0; vector < 5000; vector++ {
		var slot uint64
		hooks.BeginInto(&slot, 9, 13)
		for condition := 0; condition < 13; condition++ {
			hooks.Condition(slot, uint16(condition), vector&(1<<condition) != 0)
		}
		hooks.End(slot, vector&1 != 0)
	}`,
			evaluations: 5_000,
		},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			binary := buildWriterBenchmarkProbe(b, test.body)
			root := b.TempDir()
			dataDirs := make([]string, b.N)
			for index := range dataDirs {
				dataDirs[index] = filepath.Join(root, fmt.Sprintf("run-%06d", index))
				if err := os.Mkdir(dataDirs[index], 0o700); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(test.evaluations), "evaluations/op")
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				command := exec.Command(binary)
				command.Env = environmentWith(DataDirEnv, dataDirs[index])
				command.Env = environmentReplace(command.Env, RunIDEnv, "benchmark-run")
				if output, err := command.CombinedOutput(); err != nil {
					b.Fatalf("writer benchmark probe failed: %v\n%s", err, output)
				}
			}
			b.StopTimer()
			var journalBytes int64
			for _, dataDir := range dataDirs {
				entries, err := os.ReadDir(dataDir)
				if err != nil {
					b.Fatal(err)
				}
				for _, entry := range entries {
					info, err := entry.Info()
					if err != nil {
						b.Fatal(err)
					}
					journalBytes += info.Size()
				}
			}
			b.ReportMetric(float64(journalBytes)/float64(b.N), "journal-B/op")
		})
	}
}

func buildWriterBenchmarkProbe(b testing.TB, body string) string {
	b.Helper()
	moduleDir := b.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/writerbenchmark\n\ngo 1.26.0\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	injected, err := Inject(b.Context(), moduleDir, "example.test/writerbenchmark")
	if err != nil {
		b.Fatal(err)
	}
	source := fmt.Sprintf(`package main

import runtimecov %q

func main() {
	hooks := runtimecov.NewHooks("example.test/writerbenchmark/logic")%s
}
`, injected.ImportPath, body)
	if err := os.WriteFile(filepath.Join(moduleDir, "main.go"), []byte(source), 0o644); err != nil {
		b.Fatal(err)
	}
	binary := filepath.Join(moduleDir, "writer-benchmark")
	command := exec.Command("go", "build", "-o", binary, ".")
	command.Dir = moduleDir
	if output, err := command.CombinedOutput(); err != nil {
		b.Fatalf("build writer benchmark probe: %v\n%s", err, output)
	}
	return binary
}
