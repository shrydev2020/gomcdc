package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRegularCopiesAreIndependentAndPreserveMode(t *testing.T) {
	for _, copyFile := range []struct {
		name string
		copy func(context.Context, string, string, os.FileMode) error
	}{
		{"automatic", copyRegularFile}, {"stream", streamRegularFile},
	} {
		t.Run(copyFile.name, func(t *testing.T) {
			dir := t.TempDir()
			source, destination := filepath.Join(dir, "source"), filepath.Join(dir, "copy")
			original := bytes.Repeat([]byte("original\n"), 8192)
			if err := os.WriteFile(source, original, 0o751); err != nil {
				t.Fatal(err)
			}
			if err := copyFile.copy(t.Context(), source, destination, 0o751); err != nil {
				t.Fatal(err)
			}
			sourceInfo, _ := os.Stat(source)
			copyInfo, _ := os.Stat(destination)
			if os.SameFile(sourceInfo, copyInfo) || copyInfo.Mode().Perm() != 0o751 {
				t.Fatal("copy inode or mode not independent")
			}
			if err := os.WriteFile(destination, []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(contents, original) {
				t.Fatal("copy write changed source")
			}
			if err := copyFile.copy(t.Context(), source, destination, 0o751); !errors.Is(err, os.ErrExist) {
				t.Fatalf("existing destination error=%v", err)
			}
			contents, _ = os.ReadFile(destination)
			if string(contents) != "changed" {
				t.Fatal("existing destination was overwritten")
			}
		})
	}
}

func TestCopyRegularFileHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	destination := filepath.Join(t.TempDir(), "copy")
	if err := copyRegularFile(ctx, "unused", destination, 0o600); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination created: %v", err)
	}
}

func BenchmarkRegularFileCopy(b *testing.B) {
	for _, size := range []int{64, 16 << 20} {
		for _, method := range []struct {
			name string
			copy func(context.Context, string, string, os.FileMode) error
		}{
			{"automatic", copyRegularFile}, {"stream", streamRegularFile},
		} {
			b.Run(fmt.Sprintf("%d/%s", size, method.name), func(b *testing.B) {
				dir := b.TempDir()
				source, destination := filepath.Join(dir, "source"), filepath.Join(dir, "copy")
				if err := os.WriteFile(source, bytes.Repeat([]byte{42}, size), 0o644); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for b.Loop() {
					if err := method.copy(b.Context(), source, destination, 0o644); err != nil {
						b.Fatal(err)
					}
					if err := os.Remove(destination); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
