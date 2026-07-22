package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/modulecontext"
)

func BenchmarkCreateModuleCopy(b *testing.B) {
	source := b.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.test/benchmark\n\ngo 1.26\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	for index := 0; index < 200; index++ {
		directory := filepath.Join(source, "pkg", fmt.Sprintf("p%03d", index%20))
		if err := os.MkdirAll(directory, 0o755); err != nil {
			b.Fatal(err)
		}
		contents := []byte(fmt.Sprintf("package p%03d\n\nvar Value = %d\n", index%20, index))
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("file%03d.go", index)), contents, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	tempParent := b.TempDir()
	settings, err := modulecontext.SnapshotModule(filepath.Join(source, "go.mod"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		workspace, err := Create(b.Context(), Options{SourceConfiguration: settings, TempParent: tempParent})
		if err != nil {
			b.Fatal(err)
		}
		if err := workspace.Remove(); err != nil {
			b.Fatal(err)
		}
	}
}
