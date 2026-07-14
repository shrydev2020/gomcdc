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
		Version: "1.0", Module: "example.test/<script>alert(1)</script>",
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

func TestWriteHTMLDistinguishesAnalysisIncomplete(t *testing.T) {
	t.Parallel()
	metric := MetricSummary{Enabled: true, AnalysisIncomplete: 1}
	var output bytes.Buffer
	if err := writeHTMLReport(&output, Report{
		Module: "m", Run: Run{Status: cover.RunPassed, Complete: true},
		Summary: Summary{MCDCUnique: metric},
	}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, required := range []string{"status-analysis-incomplete", "analysis-incomplete 1"} {
		if !strings.Contains(html, required) {
			t.Fatalf("HTML omitted %q", required)
		}
	}
}

func TestReportsExposeClauseSelectionEvidence(t *testing.T) {
	t.Parallel()
	metric := MetricSummary{Enabled: true, Covered: 1, Total: 1}
	value := Report{
		Module: "m",
		Run:    Run{Status: cover.RunPassed, Complete: true},
		Packages: []PackageReport{{
			Path: "m/p",
			Files: []FileReport{{
				Path: "p.go",
				Functions: []FunctionReport{{
					Name: "Switch",
					Clauses: []ClauseReport{{
						Kind:                 cover.ClauseExpressionSwitch,
						Role:                 cover.ClauseCase,
						DirectSelections:     2,
						SelectedAlternatives: []uint16{0, 2},
						SelectionCoverage:    metric,
					}},
				}},
			}},
		}},
	}
	var output bytes.Buffer
	if err := writeHTMLReport(&output, value); err != nil {
		t.Fatal(err)
	}
	if html := output.String(); !strings.Contains(html, "direct selections 2 · alternatives [0 2]") {
		t.Fatalf("HTML omits clause-selection evidence: %s", html)
	}
	if text := RenderTextReport(value); !strings.Contains(text, "direct-selections=2 selected-alternatives=[0 2]") {
		t.Fatalf("text omits clause-selection evidence:\n%s", text)
	}
}

func TestWriteHTMLPresentsHumanReadableTriageHierarchy(t *testing.T) {
	t.Parallel()
	percentage := 75.0
	metric := MetricSummary{Enabled: true, Covered: 3, Total: 4, Percentage: &percentage}
	value := Report{
		Module: "example.test/module",
		Run:    Run{Status: cover.RunPassed, Complete: true},
		Summary: Summary{Statement: metric, Decision: metric, Condition: metric,
			MCDCUnique: metric, MCDCMasking: metric},
		Packages: []PackageReport{{Path: "example.test/module/pkg", Status: "passed", Summary: Summary{Statement: metric, Decision: metric, Condition: metric}, Files: []FileReport{{Path: "pkg/value.go", Functions: []FunctionReport{{Name: "Check", Summary: Summary{Statement: metric, Decision: metric}}}}}}},
	}
	var output bytes.Buffer
	if err := writeHTMLReport(&output, value); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, required := range []string{"Where to look first", "Open obligations", "Package → file → function → evidence", "Source view:", "Statement", "Function", "Decision", "Condition", "UC MC/DC", "Mask MC/DC", "status-partial"} {
		if !strings.Contains(html, required) {
			t.Errorf("HTML missing human-readable UI element %q", required)
		}
	}
}
