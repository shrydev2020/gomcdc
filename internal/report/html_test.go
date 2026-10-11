package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

func TestWriteHTMLRendersGoHierarchyAndEscapesSourceText(t *testing.T) {
	t.Parallel()
	location := cover.SourceLocation{File: "pkg/value.go", Start: cover.Position{Line: 7, Column: 2}, End: cover.Position{Line: 7, Column: 20}}
	metric := analysis.MetricSummary{Enabled: true, Covered: 1, Total: 2}
	percentage := 50.0
	metric.Percentage = &percentage
	value := analysis.Result{
		SchemaVersion: analysis.SchemaVersion, ToolVersion: "v2.0.0", Module: "example.test/<script>alert(1)</script>",
		Run: analysis.Run{Status: cover.RunPassed, Complete: true}, MeasurementMode: analysis.MeasurementSingleRun,
		ProducerOutcomes: []analysis.ProducerOutcome{{
			Producer: analysis.ProducerGoCover, Integrity: analysis.ProducerIntegrityValid,
			Completeness: analysis.ProducerCompletenessPartial, Mapping: analysis.ProducerMappingComplete,
			Usability: analysis.ProducerUsabilityAcceptedPartial,
		}},
		Summary:  analysis.Summary{Statement: metric},
		Packages: []analysis.PackageReport{{Path: "example.test/mod/pkg", Status: "passed", Evidence: true, Summary: analysis.Summary{Statement: metric}, Files: []analysis.FileReport{{Path: "pkg/value.go", Summary: analysis.Summary{Statement: metric}, Functions: []analysis.FunctionReport{{Name: "Check<script>", Location: &location, Summary: analysis.Summary{Statement: metric}, Decisions: []analysis.DecisionReport{{Expression: "a < b && <script>", Location: location, DecisionCoverage: analysis.DecisionCoverage{True: true}, Conditions: []analysis.ConditionReport{{Expression: "a < b", Location: location}}}}}}}}}},
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
	for _, required := range []string{"Package navigation", "Evidence producers", "go-cover", "valid", "partial", "accepted-partial", "example.test/mod/pkg", "pkg/value.go", "Check&lt;script&gt;", "a &lt; b &amp;&amp; &lt;script&gt;", "UC MC/DC", "Mask MC/DC", "gomcdc v2.0.0 (report schema 2.0)"} {
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
	if err := writeHTMLReport(&output, analysis.Result{Module: "m", Run: analysis.Run{Status: cover.RunFailed}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Partial report") {
		t.Fatal("partial report warning is missing")
	}
}

func TestWriteHTMLDistinguishesCoverageEvidenceStates(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, class, status, detail string
		metric                      analysis.MetricSummary
	}{
		{"covered", "covered", "complete", "1 / 1", analysis.MetricSummary{Enabled: true, Covered: 1, Total: 1, Percentage: new(100.0)}},
		{"unknown", "attention", "unknown", "unknown 1", analysis.MetricSummary{Enabled: true, Unknown: 1}},
		{"unsupported", "attention", "unsupported", "unsupported 1", analysis.MetricSummary{Enabled: true, Unsupported: 1}},
		{"incomplete", "attention", "analysis-incomplete", "analysis-incomplete 1", analysis.MetricSummary{Enabled: true, AnalysisIncomplete: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := WriteHTMLReport(&output, analysis.Result{
				Module: "m", Run: analysis.Run{Status: cover.RunPassed, Complete: true},
				Summary: analysis.Summary{MCDCUnique: test.metric},
			}); err != nil {
				t.Fatal(err)
			}
			card := `<div class="metric ` + test.class + `"><div class="metric-top"><div class="name">Unique-Cause MC/DC</div><span class="status status-` + test.status + `">` + test.status + `</span>`
			for _, required := range []string{card, test.detail} {
				if !strings.Contains(output.String(), required) {
					t.Errorf("HTML omitted %q", required)
				}
			}
		})
	}
}

func TestReportsExposeClauseSelectionEvidence(t *testing.T) {
	t.Parallel()
	metric := analysis.MetricSummary{Enabled: true, Covered: 1, Total: 1}
	value := analysis.Result{
		Module: "m",
		Run:    analysis.Run{Status: cover.RunPassed, Complete: true},
		Packages: []analysis.PackageReport{{
			Path: "m/p",
			Files: []analysis.FileReport{{
				Path: "p.go",
				Functions: []analysis.FunctionReport{{
					Name: "Switch",
					Clauses: []analysis.ClauseReport{{
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
	metric := analysis.MetricSummary{Enabled: true, Covered: 3, Total: 4, Percentage: &percentage}
	value := analysis.Result{
		Module: "example.test/module",
		Run:    analysis.Run{Status: cover.RunPassed, Complete: true},
		Summary: analysis.Summary{Statement: metric, Decision: metric, Condition: metric,
			MCDCUnique: metric, MCDCMasking: metric},
		Packages: []analysis.PackageReport{{Path: "example.test/module/pkg", Status: "passed", Summary: analysis.Summary{Statement: metric, Decision: metric, Condition: metric}, Files: []analysis.FileReport{{Path: "pkg/value.go", Functions: []analysis.FunctionReport{{Name: "Check", Summary: analysis.Summary{Statement: metric, Decision: metric}}}}}}},
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
