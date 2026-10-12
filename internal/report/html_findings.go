package report

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

// htmlFinding presents a coverage obligation with its source and evidence links.
type htmlFinding struct {
	ConditionIndex                                        *uint16
	Package, Function, Metric, Expression, Status, Detail string
	DecisionID, EvidenceAnchor, SourceAnchor              string
	Location                                              cover.SourceLocation
}

func htmlSourceAnchor(pkg, path string) string {
	return fmt.Sprintf("source-%x", sha256.Sum256([]byte(pkg+"\x00"+path)))
}

func htmlDecisionAnchor(pkg string, decision analysis.DecisionReport) string {
	return fmt.Sprintf("decision-%x", sha256.Sum256([]byte(pkg+"\x00"+decision.DecisionID)))
}

func htmlLocationRange(location cover.SourceLocation) string {
	if location.Start.Line == 0 {
		return location.File + " (precise location unavailable)"
	}
	return fmt.Sprintf("%s:%d:%d-%d:%d", location.File, location.Start.Line, location.Start.Column, location.End.Line, location.End.Column)
}

func htmlFindings(value analysis.Result) []htmlFinding {
	var findings []htmlFinding
	walkHTMLFindings(value, func(f htmlFinding) { findings = append(findings, f) })
	return findings
}

// walkHTMLFindings emits coverage obligations in package, file and function order.
func walkHTMLFindings(value analysis.Result, yield func(htmlFinding)) {
	for pi, pkg := range value.Packages {
		for fi, file := range pkg.Files {
			for fni, function := range file.Functions {
				walkFunctionHTMLFindings(pkg.Path, file.Path, function, fmt.Sprintf("pkg-%d-file-%d-fn-%d", pi, fi, fni), yield)
			}
		}
	}
}

func walkFunctionHTMLFindings(pkg, path string, function analysis.FunctionReport, evidenceAnchor string, yield func(htmlFinding)) {
	base := htmlFinding{Package: pkg, Function: function.Name,
		SourceAnchor:   htmlSourceAnchor(pkg, path),
		EvidenceAnchor: evidenceAnchor}
	if function.Location != nil {
		base.Location = *function.Location
	} else {
		base.Location.File = path
	}
	appendMetric := func(finding htmlFinding, name string, metric analysis.MetricSummary, detail string) {
		if !metric.Enabled || htmlSummaryMetricGaps(metric) == 0 {
			return
		}
		finding.Metric, finding.Status = name, htmlMetricStatus(metric)
		finding.Detail = fmt.Sprintf("Covered %d/%d; unknown %d; unsupported %d; infeasible %d; analysis-incomplete %d.",
			metric.Covered, metric.Total, metric.Unknown, metric.Unsupported, metric.Infeasible, metric.AnalysisIncomplete)
		if metric.Total > metric.Covered {
			finding.Detail += " " + detail
		}
		yield(finding)
	}
	appendMetric(base, "Function", function.Summary.Function, "Missing evidence: function execution.")
	for _, statement := range function.Statements {
		finding := base
		finding.Location = statement.Location
		appendMetric(finding, "Statement", statement.Metric, "Missing evidence: statement block execution.")
	}
	for _, decision := range function.Decisions {
		finding := base
		finding.Location, finding.Expression, finding.DecisionID = decision.Location, decision.Expression, decision.DecisionID
		finding.EvidenceAnchor = htmlDecisionAnchor(pkg, decision)
		appendMetric(finding, "Decision", decision.Summary.Decision, htmlMissingOutcomes(decision.DecisionCoverage.True, decision.DecisionCoverage.False))
		for _, condition := range decision.Conditions {
			finding.Location, finding.Expression = condition.Location, condition.Expression
			finding.ConditionIndex = new(condition.Index)
			appendMetric(finding, "Condition", condition.Metric, htmlMissingOutcomes(condition.True, condition.False))
			for _, strategy := range []struct {
				name    string
				enabled bool
				result  analysis.MCDCConditionReport
			}{
				{"Unique-Cause MC/DC", decision.MCDCUnique.Enabled, condition.MCDCUnique},
				{"Masking MC/DC", decision.MCDCMasking.Enabled, condition.MCDCMasking},
			} {
				if !strategy.enabled || strategy.result.Status == "disabled" || strategy.result.Status == "covered" || strategy.result.Status == "" {
					continue
				}
				f := finding
				f.Metric, f.Status = strategy.name, strategy.result.Status
				f.Detail = htmlMCDCDetail(strategy.result)
				yield(f)
			}
		}
	}
	for _, clause := range function.Clauses {
		finding := base
		finding.Location, finding.Expression = clause.Location, string(clause.Kind)+" "+string(clause.Role)
		appendMetric(finding, "Clause body", clause.BodyCoverage, "Missing evidence: clause body execution.")
		appendMetric(finding, "Clause selection", clause.SelectionCoverage, "Missing evidence: direct clause selection.")
	}
	for _, noMatch := range function.NoMatches {
		finding := base
		finding.Location, finding.Expression = noMatch.Location, noMatch.SwitchID
		appendMetric(finding, "No-match selection", noMatch.SelectionCoverage, "Missing evidence: switch selection with no matching clause.")
	}
}

func htmlSummaryMetricGaps(metric analysis.MetricSummary) int {
	return metric.Total - metric.Covered + metric.Unknown + metric.Unsupported + metric.Infeasible + metric.AnalysisIncomplete
}

func htmlMissingOutcomes(trueObserved, falseObserved bool) string {
	var missing []string
	if !trueObserved {
		missing = append(missing, "true")
	}
	if !falseObserved {
		missing = append(missing, "false")
	}
	return "Missing outcome: " + strings.Join(missing, ", ") + "."
}

func htmlMCDCDetail(result analysis.MCDCConditionReport) string {
	detail := htmlMCDCStatusDetail(result)
	if result.Reason != "" {
		detail += " Reason: " + result.Reason
	}
	return strings.TrimSpace(detail)
}

func htmlMCDCStatusDetail(result analysis.MCDCConditionReport) string {
	var detail string
	switch result.Status {
	case "not-covered":
		detail = "Missing evidence: an independence witness pair for the target condition."
	case "unknown":
		detail = "Accepted evidence is insufficient to determine coverage."
	case "unsupported", "unsupported-by-backend":
		detail = "This obligation is unsupported by the measurement or analysis."
	case "infeasible":
		detail = "Analysis determined that this obligation is infeasible."
	case "analysis-incomplete":
		detail = "Analysis did not finish determining this obligation."
	}
	if result.Witness == nil {
		detail += " No witness pair is available."
	}
	return strings.TrimSpace(detail)
}

type htmlEvidenceRow struct {
	Index                                                        uint16
	Expression, First, Second, FirstCompletion, SecondCompletion string
	Target, Masked, Unobserved                                   bool
}

type htmlWitness struct {
	analysis.MCDCConditionReport
	Strategy, Target string
	Rows             []htmlEvidenceRow
	HasCompletions   bool
	Detail           string
}

func htmlWitnessView(decision analysis.DecisionReport, condition analysis.ConditionReport, strategy string, result analysis.MCDCConditionReport) htmlWitness {
	view := htmlWitness{MCDCConditionReport: result, Strategy: strategy, Target: condition.Expression, Detail: htmlMCDCDetail(result)}
	if result.Witness == nil {
		return view
	}
	witness := result.Witness
	view.HasCompletions = len(witness.FirstCompletion) > 0 || len(witness.SecondCompletion) > 0
	for _, c := range decision.Conditions {
		view.Rows = append(view.Rows, htmlEvidenceRow{
			Index: c.Index, Expression: c.Expression, Target: c.Index == condition.Index,
			First: htmlConditionValue(witness.First.Conditions, c.Index), Second: htmlConditionValue(witness.Second.Conditions, c.Index),
			FirstCompletion: htmlConditionValue(witness.FirstCompletion, c.Index), SecondCompletion: htmlConditionValue(witness.SecondCompletion, c.Index),
			Masked: slices.Contains(witness.MaskedConditions, c.Index), Unobserved: slices.Contains(witness.UnobservedConditions, c.Index),
		})
	}
	return view
}

func htmlConditionValue(values []string, index uint16) string {
	if int(index) >= len(values) {
		return "not available"
	}
	return values[index]
}

func htmlEvaluationRows(conditions []analysis.ConditionReport, values []string) []htmlEvidenceRow {
	rows := make([]htmlEvidenceRow, 0, len(conditions))
	for _, condition := range conditions {
		rows = append(rows, htmlEvidenceRow{Index: condition.Index, Expression: condition.Expression, First: htmlConditionValue(values, condition.Index)})
	}
	return rows
}
