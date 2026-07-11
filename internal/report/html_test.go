package report

import (
	"bytes"
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

func TestWriteHTMLRendersGoHierarchyAndEscapesSourceText(t *testing.T) {
	t.Parallel()
	location := cover.SourceLocation{File: "pkg/value.go", Start: cover.Position{Line: 7, Column: 2}, End: cover.Position{Line: 7, Column: 20}}
	metric := MetricSummary{Enabled: true, Covered: 1, Total: 2}
	percentage := 50.0
	metric.Percentage = &percentage
	value := Report{
		Version: "1.0-draft", Module: "example.test/<script>alert(1)</script>",
		Run: Run{Status: cover.RunPassed, Complete: true}, MeasurementMode: MeasurementSingleRun,
		Summary:  Summary{Statement: metric},
		Packages: []PackageReport{{Path: "example.test/mod/pkg", Status: "passed", Evidence: true, Summary: Summary{Statement: metric}, Files: []FileReport{{Path: "pkg/value.go", Summary: Summary{Statement: metric}, Functions: []FunctionReport{{Name: "Check<script>", Location: &location, Summary: Summary{Statement: metric}, Decisions: []DecisionReport{{Expression: "a < b && <script>", Location: location, DecisionCoverage: DecisionCoverage{True: true}, Conditions: []ConditionReport{{Expression: "a < b", Location: location}}}}}}}}}},
	}
	var first, second bytes.Buffer
	if err := writeHTMLReport(&first, value); err != nil {
		t.Fatal(err)
	}
	if err := writeHTMLReport(&second, value); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("HTML output is not deterministic")
	}
	html := first.String()
	for _, required := range []string{"Package navigation", "example.test/mod/pkg", "pkg/value.go", "Check&lt;script&gt;", "a &lt; b &amp;&amp; &lt;script&gt;", "UC MC/DC", "Mask MC/DC"} {
		if !strings.Contains(html, required) {
			t.Errorf("HTML missing %q", required)
		}
	}
	if strings.Contains(html, "<script>") || strings.Contains(html, "http://") || strings.Contains(html, "https://") {
		t.Fatalf("HTML contains executable or external content: %s", html)
	}
}

func TestWriteHTMLMarksPartialReport(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	if err := writeHTMLReport(&output, Report{Module: "m", Run: Run{Status: cover.RunFailed}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Partial report") {
		t.Fatal("partial report warning is missing")
	}
}
