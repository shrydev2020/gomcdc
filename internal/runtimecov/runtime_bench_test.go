package runtimecov

import (
	"encoding/json"
	"os"
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
		if _, err := CollectDetailed(dataDir); err != nil {
			b.Fatal(err)
		}
	}
}
