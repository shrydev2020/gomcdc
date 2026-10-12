package report

import (
	"reflect"
	"slices"
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
		Packages: []analysis.PackageReport{{Path: "example.test/mod/pkg", Status: "passed", Evidence: true, Summary: analysis.Summary{Statement: metric}, Files: []analysis.FileReport{{Path: "pkg/value.go", Summary: analysis.Summary{Statement: metric}, Functions: []analysis.FunctionReport{{Name: "Check<script>", Location: &location, Summary: analysis.Summary{Statement: metric}, Decisions: []analysis.DecisionReport{{DecisionID: "stable", Expression: "a < b && <script>", Location: location, DecisionCoverage: analysis.DecisionCoverage{True: true}, Conditions: []analysis.ConditionReport{{Expression: "a < b", Location: location}}}}}}}}}},
	}
	first := renderHTMLPages(t, value, nil)
	second := renderHTMLPages(t, value, nil)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("HTML output is not deterministic")
	}
	html := joinHTMLPages(first)
	for _, required := range []string{"Packages", "Evidence producers", "go-cover", "valid", "partial", "accepted-partial", "example.test/mod/pkg", "pkg/value.go", "Check&lt;script&gt;", "a &lt; b &amp;&amp; &lt;script&gt;", "Unique-Cause MC/DC", "Masking MC/DC", "gomcdc v2.0.0 (report schema 2.0)"} {
		if !strings.Contains(html, required) {
			t.Errorf("HTML missing %q", required)
		}
	}
	if strings.Contains(html, "<script>") || strings.Contains(html, "http://") || strings.Contains(html, "https://") {
		t.Fatalf("HTML contains executable or external content: %s", html)
	}
}

func TestHTMLFindingsPreserveObligationStates(t *testing.T) {
	t.Parallel()
	location := cover.SourceLocation{File: "auth.go", Start: cover.Position{Line: 4, Column: 18}, End: cover.Position{Line: 4, Column: 25}}
	decision := analysis.DecisionReport{DecisionID: "stable-id", Expression: "signedIn && !expired", Location: location,
		Summary:          analysis.Summary{Decision: analysis.MetricSummary{Enabled: true, Covered: 1, Total: 2}},
		DecisionCoverage: analysis.DecisionCoverage{True: true},
		MCDCUnique:       analysis.MCDCAnalysisReport{Enabled: true}, MCDCMasking: analysis.MCDCAnalysisReport{Enabled: true},
	}
	states := []string{"not-covered", "unknown", "unsupported-by-backend", "infeasible", "analysis-incomplete", "covered", "disabled"}
	for index, state := range states {
		decision.Conditions = append(decision.Conditions, analysis.ConditionReport{
			Index: uint16(index), Expression: state + "Condition", Location: location,
			MCDCUnique:  analysis.MCDCConditionReport{Status: state, Reason: "reason-" + state},
			MCDCMasking: analysis.MCDCConditionReport{Status: "covered"},
		})
	}
	value := analysis.Result{Packages: []analysis.PackageReport{{Path: "m/auth", Files: []analysis.FileReport{{Path: "auth.go", Functions: []analysis.FunctionReport{{Name: "Check", Decisions: []analysis.DecisionReport{decision}}}}}}}}
	findings := renderHTMLPages(t, value, nil)["findings-1.html"]
	for _, required := range []string{"Function:</strong> Check", "auth.go:4:18-4:25", "Missing outcome: false.", "Missing evidence: an independence witness pair", "No witness pair is available."} {
		if !strings.Contains(findings, required) {
			t.Errorf("findings missing %q", required)
		}
	}
	for _, state := range states[:5] {
		if !strings.Contains(findings, `data-status="`+state+`"`) || !strings.Contains(findings, "reason-"+state) {
			t.Errorf("findings lost %s or its reason", state)
		}
	}
	for _, state := range states[5:] {
		if strings.Contains(findings, "reason-"+state) {
			t.Errorf("%s incorrectly listed as a finding", state)
		}
	}
	for _, finding := range collectHTMLFindings(value) {
		if finding.Status != "not-covered" && strings.Contains(finding.Detail, "Missing evidence:") {
			t.Errorf("%s incorrectly asks for missing test evidence: %s", finding.Status, finding.Detail)
		}
	}
	decision.MCDCUnique.Enabled, decision.MCDCMasking.Enabled = false, false
	decision.Summary.Decision.Enabled = false
	value.Packages[0].Files[0].Functions[0].Decisions[0] = decision
	if got := collectHTMLFindings(value); len(got) != 0 {
		t.Fatalf("disabled metrics produced findings: %#v", got)
	}
}

func TestHTMLWitnessNamesConditionsAndSeparatesMaskingCompletions(t *testing.T) {
	t.Parallel()
	witness := &analysis.WitnessReport{
		First:           analysis.EvaluationReport{Conditions: []string{"true", "not-evaluated"}, Result: false, Status: "completed", TestID: "TestExpired", EvaluationID: "eval-a", RunID: "run-a", PackagePath: "m/auth", ProcessID: 42},
		Second:          analysis.EvaluationReport{Conditions: []string{"true", "false"}, Result: true, Status: "completed", TestID: "TestValid", EvaluationID: "eval-b", RunID: "run-b", PackagePath: "m/auth", ProcessID: 43},
		FirstCompletion: []string{"true", "true"}, SecondCompletion: []string{"true", "false"}, MaskedConditions: []uint16{0}, UnobservedConditions: []uint16{1},
	}
	condition := analysis.ConditionReport{Index: 1, Expression: "expired", MCDCMasking: analysis.MCDCConditionReport{Status: "covered", Outcome: "covered", Support: "supported", Analysis: "complete", Witness: witness}}
	decision := analysis.DecisionReport{DecisionID: "stable", Expression: "signedIn && !expired", MCDCMasking: analysis.MCDCAnalysisReport{Enabled: true},
		Conditions:  []analysis.ConditionReport{condition, {Index: 0, Expression: "signedIn", MCDCMasking: analysis.MCDCConditionReport{Status: "not-covered"}}},
		Evaluations: []analysis.EvaluationReport{{Conditions: []string{"true"}, Status: "aborted", TestID: "TestPanic"}},
	}
	value := analysis.Result{Packages: []analysis.PackageReport{{Path: "m/auth", Files: []analysis.FileReport{{Path: "auth.go", Functions: []analysis.FunctionReport{{Decisions: []analysis.DecisionReport{decision}}}}}}}}
	output := joinHTMLPages(renderHTMLPages(t, value, nil))
	for _, required := range []string{"Target condition:</strong> expired", "Evaluation A (observed)", "Completion A (analysis)", "#1 expired</th><td>not-evaluated</td><td>false</td><td>true</td>", "Witness test", "TestExpired", "TestValid", "eval-a", "run-b", "process 42", "not available (aborted)", "not available"} {
		if !strings.Contains(output, required) {
			t.Errorf("HTML witness missing %q", required)
		}
	}
}

func TestHTMLAnchorsDoNotDependOnPackageOrder(t *testing.T) {
	t.Parallel()
	decision := analysis.DecisionReport{DecisionID: "0x1234", Expression: "a"}
	value := analysis.Result{Packages: []analysis.PackageReport{
		{Path: "m/a", Files: []analysis.FileReport{{Path: "a.go", Functions: []analysis.FunctionReport{{Decisions: []analysis.DecisionReport{decision}}}}}},
		{Path: "m/b", Files: []analysis.FileReport{{Path: "b.go", Functions: []analysis.FunctionReport{{Decisions: []analysis.DecisionReport{decision}}}}}},
	}}
	anchors := []string{htmlDecisionAnchor("m/a", decision), htmlDecisionAnchor("m/b", decision), htmlSourceAnchor("m/a", "a.go")}
	if anchors[0] == anchors[1] {
		t.Fatal("decision anchor collides across packages")
	}
	for range 2 {
		pages := renderHTMLPages(t, value, nil)
		for _, anchor := range anchors {
			if _, ok := pages[anchor+"-1.html"]; !ok {
				t.Errorf("missing page %s", anchor)
			}
		}
		slices.Reverse(value.Packages)
	}
}

func TestWriteHTMLMarksPartialReport(t *testing.T) {
	t.Parallel()
	output := renderHTMLPages(t, analysis.Result{Module: "m", Run: analysis.Run{Status: cover.RunFailed}}, nil)["overview-1.html"]
	if !strings.Contains(output, "Partial report") {
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
			output := renderHTMLPages(t, analysis.Result{Module: "m", Run: analysis.Run{Status: cover.RunPassed, Complete: true}, Summary: analysis.Summary{MCDCUnique: test.metric}}, nil)["overview-1.html"]
			card := `<div class="metric ` + test.class + `"><div class="name" title="Unique-Cause MC/DC">Unique-Cause MC/DC</div><span class="status status-` + test.status + `">` + test.status + `</span>`
			for _, required := range []string{card, test.detail} {
				if !strings.Contains(output, required) {
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
	output := joinHTMLPages(renderHTMLPages(t, value, nil))
	if html := output; !strings.Contains(html, "direct selections 2") || !strings.Contains(html, "selected alternative 0") || !strings.Contains(html, "selected alternative 2") {
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
	output := joinHTMLPages(renderHTMLPages(t, value, nil))
	html := output
	for _, required := range []string{"Uncovered obligations", "open obligations", "Browse packages → files → functions", "Original source", "Statement", "Function", "Decision", "Condition", "Unique-Cause MC/DC", "Masking MC/DC", "status-partial"} {
		if !strings.Contains(html, required) {
			t.Errorf("HTML missing human-readable UI element %q", required)
		}
	}
}

func renderHTMLPages(t *testing.T, value analysis.Result, sources []analysis.SourceFileInput) map[string]string {
	t.Helper()
	dir := t.TempDir()
	if err := WriteHTMLDirectory(t.Context(), dir, value, sources); err != nil {
		t.Fatal(err)
	}
	_, pages := readHTMLGeneration(t, dir)
	assertHTMLLinksAndBudgets(t, pages)
	return pages
}
func joinHTMLPages(pages map[string]string) string {
	var b strings.Builder
	for _, body := range pages {
		b.WriteString(body)
	}
	return b.String()
}
func collectHTMLFindings(value analysis.Result) []htmlFinding {
	var result []htmlFinding
	for _, pkg := range value.Packages {
		for _, file := range pkg.Files {
			for _, fn := range file.Functions {
				walkFunctionHTMLFindings(pkg.Path, file.Path, fn, htmlFunctionID(pkg.Path, file.Path, fn), func(f htmlFinding) { result = append(result, f) })
			}
		}
	}
	return result
}
