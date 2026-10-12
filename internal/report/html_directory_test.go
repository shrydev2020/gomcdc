package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

func directoryFixture(observations, conditions int) (analysis.Result, []analysis.SourceFileInput) {
	source := []byte("package p\n// 日本語 <script> &\nfunc Check(a bool) bool { return a }\n")
	start := bytes.Index(source, []byte("return a"))
	loc := cover.SourceLocation{File: "p.go", StartOffset: start, EndOffset: start + 8, Start: cover.Position{Line: 3, Column: 27}, End: cover.Position{Line: 3, Column: 35}}
	m := analysis.MetricSummary{Enabled: true, Covered: 1, Total: 2, Percentage: new(50.0)}
	d := analysis.DecisionReport{DecisionID: "d", Expression: "a && b", Location: loc, Summary: analysis.Summary{Decision: m}, DecisionCoverage: analysis.DecisionCoverage{True: true}, MCDCUnique: analysis.MCDCAnalysisReport{Enabled: true}, MCDCMasking: analysis.MCDCAnalysisReport{Enabled: true}}
	for i := range conditions {
		d.Conditions = append(d.Conditions, analysis.ConditionReport{ConditionID: fmt.Sprintf("c%d", i), Index: uint16(i), Expression: fmt.Sprintf("condition%d", i), Location: loc, Metric: m, MCDCUnique: analysis.MCDCConditionReport{Status: "not-covered"}, MCDCMasking: analysis.MCDCConditionReport{Status: "analysis-incomplete", Reason: "budget exhausted"}})
	}
	for i := range observations {
		e := analysis.EvaluationReport{DecisionID: "d", EvaluationID: fmt.Sprintf("e%d", i), TestID: fmt.Sprintf("Test%d\t<script>\n日本語", i), RunID: "run", PackagePath: "m/p", ProcessID: i, Status: "completed", Result: i%2 == 0}
		for c := range conditions {
			e.Conditions = append(e.Conditions, []string{"true", "false", "not-evaluated"}[(i+c)%3])
		}
		d.Evaluations = append(d.Evaluations, e)
	}
	if observations >= 2 && conditions > 0 {
		d.Conditions[0].MCDCMasking = analysis.MCDCConditionReport{Status: "covered", Outcome: "covered", Support: "supported", Analysis: "complete", Witness: &analysis.WitnessReport{First: d.Evaluations[0], Second: d.Evaluations[1], FirstCompletion: slices.Repeat([]string{"true"}, conditions), SecondCompletion: slices.Repeat([]string{"false"}, conditions), MaskedConditions: []uint16{1}, UnobservedConditions: []uint16{2}}}
	}
	value := analysis.Result{Module: "m", SchemaVersion: "2.0", ToolVersion: "test", Run: analysis.Run{Status: cover.RunPassed, Complete: true}, Summary: analysis.Summary{Decision: m}, Packages: []analysis.PackageReport{{Path: "m/p", Summary: analysis.Summary{Decision: m}, Files: []analysis.FileReport{{Path: "p.go", Functions: []analysis.FunctionReport{{Name: "Check", Location: &loc, Summary: analysis.Summary{Decision: m}, Decisions: []analysis.DecisionReport{d}}}}}}}}
	return value, []analysis.SourceFileInput{{PackagePath: "m/p", Path: "p.go", Source: source}}
}
func readHTMLGeneration(t *testing.T, dir string) (string, map[string]string) {
	t.Helper()
	entry, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	link := htmlHref.FindStringSubmatch(string(entry))
	if len(link) != 2 {
		t.Fatal("entry link missing")
	}
	name, _, found := strings.Cut(link[1], "/")
	if !found || !strings.HasPrefix(name, "report-") {
		t.Fatalf("invalid generation link %q", link[1])
	}
	generation := filepath.Join(dir, name)
	files := map[string]string{}
	entries, err := os.ReadDir(generation)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(generation, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(b)
	}
	return generation, files
}
func assertHTMLLinksAndBudgets(t *testing.T, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		if len(body) > htmlPageBytes || htmlElements(body) > htmlPageElements {
			t.Fatalf("unbounded page %s: %d bytes / %d elements", name, len(body), htmlElements(body))
		}
		if strings.Contains(body, "<script") {
			t.Fatalf("executable source in %s", name)
		}
		for _, m := range htmlHref.FindAllStringSubmatch(body, -1) {
			dest, fragment, _ := strings.Cut(html.UnescapeString(m[1]), "#")
			target, ok := files[dest]
			if !ok {
				t.Fatalf("%s links to missing %s", name, dest)
			}
			if fragment != "" && !strings.Contains(target, `id="`+fragment+`"`) {
				t.Fatalf("%s links to missing fragment %s in %s", name, fragment, dest)
			}
		}
	}
}
func TestHTMLDirectoryPreservesEvidenceAndBoundsBothTableAxes(t *testing.T) {
	value, sources := directoryFixture(451, 35)
	d := &value.Packages[0].Files[0].Functions[0].Decisions[0]
	slices.Reverse(d.Conditions)
	d.Evaluations[17].Status = "aborted"
	d.Evaluations[17].Conditions = d.Evaluations[17].Conditions[:4]
	d.Evaluations[18].EvaluationID = d.Evaluations[0].EvaluationID
	d.Evaluations[18].RunID = "another-run"
	before, _ := json.Marshal(value)
	dir := t.TempDir()
	if err := WriteHTMLDirectory(t.Context(), dir, value, sources); err != nil {
		t.Fatal(err)
	}
	_, files := readHTMLGeneration(t, dir)
	assertHTMLLinksAndBudgets(t, files)
	after, _ := json.Marshal(value)
	if !bytes.Equal(before, after) {
		t.Fatal("renderer changed analysis")
	}
	index, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(index, []byte("data-observation")) || bytes.Contains(index, []byte("condition34")) {
		t.Fatal("overview eagerly contains evidence")
	}
	rowRE := regexp.MustCompile(`(?s)<tr data-observation="(\d+)">(.*?)</tr>`)
	cellRE := regexp.MustCompile(`(?s)<t[dh][^>]*>(.*?)</t[dh]>`)
	strip := regexp.MustCompile(`<[^>]+>`)
	seen := map[int]int{}
	for name, body := range files {
		if !strings.Contains(name, "-evaluations-cols-") {
			continue
		}
		groupText := strings.Split(strings.Split(name, "-evaluations-cols-")[1], "-")[0]
		start, _ := strconv.Atoi(groupText)
		end := min(start+16, len(d.Conditions))
		for _, m := range rowRE.FindAllStringSubmatch(body, -1) {
			ordinal, _ := strconv.Atoi(m[1])
			seen[ordinal]++
			cells := cellRE.FindAllStringSubmatch(m[2], -1)
			e := d.Evaluations[ordinal-1]
			want := []string{fmt.Sprint(ordinal), e.EvaluationID, e.TestID, e.Status}
			for _, c := range d.Conditions[start:end] {
				want = append(want, htmlConditionValue(e.Conditions, c.Index))
			}
			want = append(want, evaluationOutcome(e), e.RunID, e.PackagePath, fmt.Sprint(e.ProcessID))
			if len(cells) != len(want) {
				t.Fatalf("observation %d has %d cells, want %d", ordinal, len(cells), len(want))
			}
			for i, cell := range cells {
				got := html.UnescapeString(strip.ReplaceAllString(cell[1], ""))
				if got != want[i] {
					t.Fatalf("observation %d column %d = %q, want %q", ordinal, i, got, want[i])
				}
			}
		}
	}
	if len(seen) != 451 {
		t.Fatalf("retained %d observations", len(seen))
	}
	for i, n := range seen {
		if n != 3 {
			t.Fatalf("observation %d appears in %d condition groups, want 3", i, n)
		}
	}
	var witness strings.Builder
	for name, b := range files {
		if strings.Contains(name, "-masking-") {
			witness.WriteString(b)
		}
	}
	for _, text := range []string{"Evaluation A (observed)", "Completion A (analysis)", "not-evaluated", "masked", "unobserved", "Target occurrence #0", "process 1"} {
		if !strings.Contains(witness.String(), text) {
			t.Errorf("witness missing %q", text)
		}
	}
}
func TestHTMLDirectoryHugeSourceAndLongFieldsAreLossless(t *testing.T) {
	value, sources := directoryFixture(0, 1)
	// Exercise both many lines and a single giant line, including escaped UTF-8.
	sources[0].Source = []byte(strings.Repeat("// 日本語<&>\r\n", 1200) + strings.Repeat("大<&>", 300000))
	long := strings.Repeat("long-test<&>日本語\t\n", 50000)
	value.Packages[0].Files[0].Functions[0].Decisions[0].Conditions[0].MCDCMasking.Reason = long
	dir := t.TempDir()
	if err := WriteHTMLDirectory(t.Context(), dir, value, sources); err != nil {
		t.Fatal(err)
	}
	_, files := readHTMLGeneration(t, dir)
	assertHTMLLinksAndBudgets(t, files)
	sourceID := htmlSourceAnchor("m/p", "p.go")
	segment := regexp.MustCompile(`(?s)<span class="src-segment [^"]*" id="byte-(\d+)" data-start-offset="(\d+)" data-end-offset="(\d+)"[^>]*>(.*?)</span>`)
	type span struct {
		start, end int
		text       string
	}
	var spans []span
	for name, b := range files {
		if strings.HasPrefix(name, sourceID+"-") {
			for _, m := range segment.FindAllStringSubmatch(b, -1) {
				a, _ := strconv.Atoi(m[2])
				z, _ := strconv.Atoi(m[3])
				spans = append(spans, span{a, z, html.UnescapeString(m[4])})
			}
		}
	}
	slices.SortFunc(spans, func(a, b span) int { return a.start - b.start })
	var restored strings.Builder
	for _, span := range spans {
		if restored.Len() != span.start || len(span.text) != span.end-span.start {
			t.Fatal("source gap, duplication, or byte offset loss")
		}
		restored.WriteString(span.text)
	}
	if restored.String() != string(sources[0].Source) {
		t.Fatal("original source did not round trip")
	}
	id := htmlID("text", long)
	pre := regexp.MustCompile(`(?s)<pre class="evidence-context">(.*?)</pre>`)
	var reason strings.Builder
	for page := 1; ; page++ {
		b, ok := files[htmlPageName(id, page)]
		if !ok {
			break
		}
		m := pre.FindStringSubmatch(b)
		if len(m) != 2 {
			t.Fatal("text continuation missing")
		}
		reason.WriteString(html.UnescapeString(m[1]))
	}
	if !strings.Contains(reason.String(), long) {
		t.Fatal("long reason truncated")
	}
}
func TestHTMLDirectoryPublishesCompleteGenerations(t *testing.T) {
	value, sources := directoryFixture(3, 2)
	dir := t.TempDir()
	if err := WriteHTMLDirectory(t.Context(), dir, value, sources); err != nil {
		t.Fatal(err)
	}
	generation, files := readHTMLGeneration(t, dir)
	if err := WriteHTMLDirectory(t.Context(), dir, value, sources); err != nil {
		t.Fatal(err)
	}
	next, again := readHTMLGeneration(t, dir)
	if next == generation || !maps.Equal(files, again) {
		t.Fatal("generation content or isolation changed")
	}
	entry, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		ctx     context.Context
		sources []analysis.SourceFileInput
	}{
		{ctx, sources}, {t.Context(), append(slices.Clone(sources), sources[0])},
	} {
		if err := WriteHTMLDirectory(test.ctx, dir, value, test.sources); err == nil {
			t.Fatal("invalid publication succeeded")
		}
		after, err := os.ReadFile(filepath.Join(dir, "index.html"))
		if err != nil || !bytes.Equal(entry, after) {
			t.Fatal("failed publication replaced entry", err)
		}
	}
	for name, body := range files {
		preserved, err := os.ReadFile(filepath.Join(generation, name))
		if err != nil || string(preserved) != body {
			t.Fatalf("old page %s changed: %v", name, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("temporary output retained: %s", e.Name())
		}
	}
}
func TestHTMLColorsMatchExistingSeverityWithNestedAnnotations(t *testing.T) {
	var annotations []SourceAnnotation
	states := []string{"covered", "partial", "not-covered", "unknown", "unsupported", "disabled", "infeasible", "analysis-incomplete", "both"}
	metrics := []string{"statement", "decision", "condition", "mcdc-unique", "mcdc-masking", "clause-body", "clause-selection"}
	for i := range 200 {
		annotations = append(annotations, SourceAnnotation{StartOffset: i, EndOffset: 401 - i, Metric: metrics[i%len(metrics)], State: states[i%len(states)]})
	}
	walkHTMLColors(402, annotations, func(start, end int, classes string) bool {
		var active []*SourceAnnotation
		for i := range annotations {
			a := &annotations[i]
			if a.StartOffset <= start && a.EndOffset >= end {
				active = append(active, a)
			}
		}
		for _, mode := range htmlSourceModes() {
			want := htmlSourceSeverity(active, mode)
			if want != "none" && !strings.Contains(classes, "view-"+mode+"-"+want) {
				t.Fatalf("range %d–%d mode %s: %q, want %s", start, end, mode, classes, want)
			}
		}
		return true
	})
}
func BenchmarkHTMLColorsNested(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			annotations := make([]SourceAnnotation, n)
			for i := range annotations {
				annotations[i] = SourceAnnotation{StartOffset: i, EndOffset: 2*n - i, Metric: "condition", State: "covered"}
			}
			b.ReportAllocs()
			for b.Loop() {
				walkHTMLColors(2*n, annotations, func(_, _ int, _ string) bool { return true })
			}
		})
	}
}

func TestHTMLDirectoryOverviewDoesNotGrowWithPackageCount(t *testing.T) {
	var baseline string
	for _, count := range []int{10, 1000} {
		value := analysis.Result{Module: "large", SchemaVersion: "2.0"}
		for i := range count {
			value.Packages = append(value.Packages, analysis.PackageReport{Path: fmt.Sprintf("large/pkg%d", i)})
		}
		dir := t.TempDir()
		if err := WriteHTMLDirectory(t.Context(), dir, value, nil); err != nil {
			t.Fatal(err)
		}
		_, files := readHTMLGeneration(t, dir)
		assertHTMLLinksAndBudgets(t, files)
		overview := files["overview-1.html"]
		if baseline == "" {
			baseline = overview
		} else if overview != baseline {
			t.Fatal("overview contains package-proportional data")
		}
	}
}

func htmlSourceSeverity(annotations []*SourceAnnotation, mode string) string {
	severity := 0
	for _, a := range annotations {
		matches := a.Metric == mode || mode == "combined" || mode == "mcdc" && (a.Metric == "mcdc-unique" || a.Metric == "mcdc-masking") || mode == "clause" && (a.Metric == "clause-body" || a.Metric == "clause-selection")
		if matches {
			severity = max(severity, htmlStateSeverity(a.State))
		}
	}
	return []string{"none", "covered", "partial", "uncovered", "attention"}[severity]
}
