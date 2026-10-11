package resultfile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Cancel when a completed temporary snapshot reaches the publication check.
// The filesystem condition avoids timer races and dependence on Err call counts.
type cancelAtPublication struct {
	context.Context
	cancel context.CancelFunc
	root   string
	staged []byte
	err    error
}

func (ctx *cancelAtPublication) Err() error {
	paths, err := filepath.Glob(filepath.Join(ctx.root, ".gomcdc-result-*"))
	if err != nil {
		ctx.err = err
	}
	if len(paths) == 1 {
		ctx.staged, ctx.err = os.ReadFile(paths[0])
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestCancellationAfterStagingPreservesPublishedSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	destination := filepath.Join(root, "result.json")
	previous := fixture()
	if err := Write(destination, previous); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	replacement := fixture()
	replacement.Report.ToolVersion = "replacement"
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancelAtPublication{Context: parent, cancel: cancel, root: root}
	if err := WriteContext(ctx, destination, replacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("publication cancellation: %v", err)
	}
	staged, err := Decode(bytes.NewReader(ctx.staged))
	if ctx.err != nil || err != nil || staged.Report.ToolVersion != replacement.Report.ToolVersion {
		t.Fatalf("cancellation must follow staging a complete replacement: %v %v", ctx.err, err)
	}
	after, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("previous snapshot changed: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "result.json" {
		t.Fatalf("staged file cleanup: %v %v", entries, err)
	}
}
