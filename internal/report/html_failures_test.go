package report

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

func TestHTMLPublicationRetainsBrokenLinkCause(t *testing.T) {
	value, sources := directoryFixture(1, 1)
	dir := t.TempDir()
	if err := WriteHTMLDirectory(t.Context(), dir, value, sources); err != nil {
		t.Fatal(err)
	}
	generation, _ := readHTMLGeneration(t, dir)
	entry, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(generation, "packages-1.html")); err != nil {
		t.Fatal(err)
	}
	err = WriteHTMLDirectory(t.Context(), dir, value, sources)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error lost missing-file cause: %v", err)
	}
	for _, context := range []string{"validate existing HTML generation", generation, "packages-1.html", "resolve link"} {
		if !strings.Contains(err.Error(), context) {
			t.Errorf("error missing %q: %v", context, err)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(entry, after) {
		t.Fatal("failed publication replaced entry")
	}
}

func TestHTMLWriterRetainsFirstFailure(t *testing.T) {
	output := &htmlPageWriter{ctx: t.Context(), dir: t.TempDir()}
	output.write("same.html", []byte("first"))
	output.write("same.html", []byte("second"))
	first := output.err
	if !errors.Is(first, fs.ErrExist) || !strings.Contains(first.Error(), "same.html") {
		t.Fatalf("missing create failure context: %v", first)
	}
	output.field(strings.Repeat("long text", 1000))
	output.write("later.html", []byte("third"))
	if output.err != first {
		t.Fatalf("replaced first failure: %v", output.err)
	}
	content, err := os.ReadFile(filepath.Join(output.dir, "same.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first" {
		t.Fatalf("overwrote asset: %q", content)
	}
	if _, err := os.Stat(filepath.Join(output.dir, "later.html")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("wrote after failure: %v", err)
	}
}

func TestHTMLRejectsAmbiguousInputsBeforePublication(t *testing.T) {
	for _, kind := range []string{"duplicate source", "missing decision ID"} {
		t.Run(kind, func(t *testing.T) {
			value, sources := directoryFixture(1, 1)
			if kind == "duplicate source" {
				sources = append(sources, sources[0])
			} else {
				value.Packages[0].Files[0].Functions[0].Decisions[0].DecisionID = ""
			}
			dir := t.TempDir()
			err := WriteHTMLDirectory(t.Context(), dir, value, sources)
			if err == nil {
				t.Fatal("ambiguous input published")
			}
			if kind == "duplicate source" && !strings.Contains(err.Error(), "duplicate original source") {
				t.Fatal(err)
			}
			if kind == "missing decision ID" && !strings.Contains(err.Error(), kind) {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "index.html")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("entry exists: %v", err)
			}
		})
	}
}

func TestHTMLMissingSourceLocationDisplaysDiagnostic(t *testing.T) {
	value, _ := directoryFixture(1, 1)
	dir := t.TempDir()
	if err := WriteHTMLDirectory(t.Context(), dir, value, nil); err != nil {
		t.Fatal(err)
	}
	_, files := readHTMLGeneration(t, dir)
	assertHTMLLinksAndBudgets(t, files)
	fn := value.Packages[0].Files[0].Functions[0]
	body := files[htmlPageName(htmlFunctionID("m/p", "p.go", fn), 1)]
	if !strings.Contains(body, "Source mapping:") {
		t.Fatal("mapping diagnostic missing")
	}
	if strings.Contains(body, `href="source-`) {
		t.Fatal("unmapped location links to unrelated source page")
	}
}

func TestHTMLSourceOffsetRequiresContainingPageAndAnchor(t *testing.T) {
	for _, view := range []htmlSourcePages{
		{id: "empty"},
		{id: "gap", ranges: []htmlSourceRange{{start: 5, end: 10, page: 1}}, anchors: []int{5}},
		{id: "anchor", ranges: []htmlSourceRange{{start: 0, end: 10, page: 1}}, anchors: []int{5}},
	} {
		if target, err := view.offset(1); err == nil {
			t.Fatalf("%s guessed target %q", view.id, target)
		}
	}
	view := htmlSourcePages{id: "source", ranges: []htmlSourceRange{{start: 0, end: 5, page: 1}, {start: 5, end: 10, page: 2}}, anchors: []int{0, 3, 5, 8}}
	if target, err := view.offset(7); err != nil || target != "source-2.html#byte-5" {
		t.Fatalf("target %q: %v", target, err)
	}
}

func TestHTMLColorsRejectUnknownMetricStateAndRange(t *testing.T) {
	for _, annotation := range []SourceAnnotation{
		{Metric: "future-metric", State: "covered", EndOffset: 1},
		{Metric: "condition", State: "future-state", EndOffset: 1},
		{Metric: "condition", State: "covered", EndOffset: 99},
	} {
		called := false
		err := walkHTMLColors(2, []SourceAnnotation{annotation}, func(_, _ int, _ string) bool { called = true; return true })
		if err == nil || called {
			t.Fatalf("invalid annotation rendered: %+v, err %v", annotation, err)
		}
	}
}

func TestHTMLCancellationPreservesCause(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	value, sources := directoryFixture(0, 0)
	err := WriteHTMLDirectory(ctx, t.TempDir(), value, sources)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "start HTML report") {
		t.Fatalf("cancellation lost context or cause: %v", err)
	}
}

func TestSourceMappingRejectsInvalidExplicitCoordinates(t *testing.T) {
	source := []byte("abc\ndef\n")
	loc := cover.SourceLocation{Start: cover.Position{Line: 1, Column: 1}, End: cover.Position{Line: 1, Column: 3}, StartOffset: 100, EndOffset: 102}
	if m := sourceRangeOffsets(loc, source); m.status != "unknown" {
		t.Fatalf("invalid offsets replaced with line columns: %+v", m)
	}
	loc.StartOffset, loc.EndOffset = 0, 0
	if m := sourceRangeOffsets(loc, source); m.status != "mapped" || m.start != 0 || m.end != 2 {
		t.Fatalf("line-column coordinates: %+v", m)
	}
	loc.End.Column = 6
	if m := sourceRangeOffsets(loc, source); m.status != "unknown" {
		t.Fatalf("column crossed line boundary: %+v", m)
	}
}

func TestHTMLLongInvalidTextReturnsError(t *testing.T) {
	output := &htmlPageWriter{ctx: t.Context(), dir: t.TempDir()}
	output.field(strings.Repeat("\x80", htmlTextChunkBytes+1))
	if output.err == nil || !strings.Contains(output.err.Error(), "invalid UTF-8 boundary") {
		t.Fatalf("invalid text error: %v", output.err)
	}
}
