package report

import (
	"html"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

func htmlVisibleText(body string) string {
	return html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " "))
}

func TestHTMLDetailPagesIdentifyTheirEvidence(t *testing.T) {
	value, sources := directoryFixture(451, 35)
	d := &value.Packages[0].Files[0].Functions[0].Decisions[0]
	slices.Reverse(d.Conditions)
	dir := t.TempDir()
	if err := WriteHTMLDirectory(t.Context(), dir, value, sources); err != nil {
		t.Fatal(err)
	}
	_, files := readHTMLGeneration(t, dir)
	assertHTMLLinksAndBudgets(t, files)
	var decisions, conditions, witnesses, observations int
	for name, body := range files {
		switch {
		case strings.Contains(body, `<h1>Decision and conditions</h1>`):
			decisions++
		case strings.Contains(body, `<h1>Condition evidence</h1>`):
			conditions++
		case strings.Contains(body, `<h1>Masking MC/DC witness</h1>`):
			witnesses++
		case strings.Contains(body, `<h1>Observed evaluations</h1>`):
			observations++
		default:
			continue
		}
		text := strings.Join(strings.Fields(htmlVisibleText(body)), " ")
		for _, want := range []string{"Package m/p", "File p.go", "Function Check", "Decision a && b", "Decision ID d", "Decision location p.go:3"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks visible context %q", name, want)
			}
		}
		if strings.Contains(body, `<h1>Condition evidence</h1>`) && strings.Contains(text, "Target condition condition0 ") {
			// Reversed presentation order puts occurrence zero in the final column group.
			if !strings.Contains(body, `-evaluations-cols-32-1.html`) {
				t.Errorf("%s links to the wrong target column group", name)
			}
		}
	}
	if decisions == 0 || conditions < 35 || witnesses == 0 || observations < 10 {
		t.Fatalf("missing evidence or continuation pages: %d decisions, %d conditions, %d witnesses, %d observations", decisions, conditions, witnesses, observations)
	}
	text := htmlVisibleText(files["overview-1.html"])
	for _, name := range []string{"Statement", "Decision", "Condition", "Unique-Cause MC/DC", "Masking MC/DC", "Switch clause body", "Type switch clause body", "Select clause body", "Switch clause selection", "Type switch clause selection", "Function"} {
		if !strings.Contains(text, name) {
			t.Errorf("overview lacks visible metric name %q", name)
		}
	}
}

func TestHTMLMCDCRequirementsRespectAnalysisStatus(t *testing.T) {
	for _, status := range []string{"not-covered", "unknown", "unsupported", "infeasible", "analysis-incomplete"} {
		t.Run(status, func(t *testing.T) {
			value, sources := directoryFixture(1, 1)
			d := &value.Packages[0].Files[0].Functions[0].Decisions[0]
			m := analysis.MCDCConditionReport{Status: status, Reason: "retained analysis reason"}
			d.Conditions[0].MCDCUnique, d.Conditions[0].MCDCMasking = m, m
			dir := t.TempDir()
			if err := WriteHTMLDirectory(t.Context(), dir, value, sources); err != nil {
				t.Fatal(err)
			}
			_, files := readHTMLGeneration(t, dir)
			found := false
			for _, body := range files {
				if !strings.Contains(body, `<h1>Condition evidence</h1>`) {
					continue
				}
				found = true
				text := htmlVisibleText(body)
				wantRequirements := 0
				if status == "not-covered" {
					wantRequirements = 2
				}
				if strings.Count(text, "Evidence to establish") != wantRequirements {
					t.Errorf("status %s has inappropriate requirements", status)
				}
				if !strings.Contains(text, status) || strings.Count(text, m.Reason) != 2 {
					t.Error("analysis status or reason changed")
				}
				if status == "not-covered" {
					for _, want := range []string{"same recorded value", "Flipping the target alone", "masked in both completions", "Inspect observations for target condition #0"} {
						if !strings.Contains(text, want) {
							t.Errorf("missing evidence guidance %q", want)
						}
					}
				}
			}
			if !found {
				t.Fatal("condition page missing")
			}
		})
	}
}

func TestHTMLTargetObservationsKeepRepetitionsAndIncompleteStates(t *testing.T) {
	s := &htmlReportRenderer{output: &htmlPageWriter{}}
	d := analysis.DecisionReport{DecisionID: "d", Evaluations: []analysis.EvaluationReport{
		{Status: "completed", Conditions: []string{"false"}, Result: false},
		{Status: "completed", Conditions: []string{"false"}, Result: false},
		{Status: "completed", Conditions: []string{"false"}, Result: true},
		{Status: "completed", Conditions: []string{"true"}, Result: false},
		{Status: "completed", Conditions: []string{"true"}, Result: true},
		{Status: "completed", Conditions: []string{"not-evaluated"}, Result: true},
		{Status: "completed"},
		{Status: "aborted", Conditions: []string{"false"}},
	}}
	body := s.targetObservations(d, analysis.ConditionReport{Index: 0})
	if s.output.err != nil {
		t.Fatal(s.output.err)
	}
	for _, want := range []string{
		`<tr><th scope="row">false</th><td>2</td><td>1</td></tr>`,
		`<tr><th scope="row">true</th><td>1</td><td>1</td></tr>`,
		"Short-circuited target: 1", "unavailable target value: 1", "incomplete evaluations: 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("observation summary lacks %q", want)
		}
	}
	d.Evaluations[0].Conditions[0] = "invalid-state"
	s.targetObservations(d, analysis.ConditionReport{Index: 0})
	if s.output.err == nil || !strings.Contains(s.output.err.Error(), `invalid value "invalid-state"`) {
		t.Fatalf("invalid value was silently summarized: %v", s.output.err)
	}
}
