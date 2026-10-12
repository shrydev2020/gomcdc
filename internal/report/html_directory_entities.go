package report

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

func htmlFunctionID(pkg, path string, fn analysis.FunctionReport) string {
	loc := ""
	if fn.Location != nil {
		loc = htmlLocationRange(*fn.Location)
	}
	return htmlID("function", pkg, path, fn.Name, loc)
}
func (s *htmlDirectory) render(value analysis.Result) {
	overview := s.pages("overview", "Coverage overview", "", "", "", "", htmlIndexRows)
	overview.add(s.paragraph("Module", value.Module) + s.paragraph("Run status", string(value.Run.Status)) + s.paragraph("Evidence", completeness(value.Run.Complete)) + s.paragraph("Measurement", string(value.MeasurementMode)))
	overview.add(s.paragraph("Run results", fmt.Sprintf("test %s · measurement %s · integrity %s · strict %s · threshold %s", value.Run.Results.Test, value.Run.Results.Measurement, value.Run.Results.Integrity, value.Run.Results.Strict, value.Run.Results.Threshold)))
	if !value.Run.Complete {
		overview.add(`<p class="notice" role="status"><strong>Partial report.</strong> Coverage reflects only validated evidence.</p>`)
	}
	overview.add(s.metrics(value.Summary))
	if value.MaskingAnalysisLimits != nil {
		l := value.MaskingAnalysisLimits
		overview.add(fmt.Sprintf(`<p>Masking limits per condition: %d evaluation pairs · %d search states · %d solver bytes.</p>`, l.MaxEvaluationPairs, l.MaxSearchStates, l.MaxSolverBytes))
	}
	overview.add(`<div class="legend">covered · partial · not-covered · unknown · unsupported · infeasible · analysis-incomplete</div>`)
	overview.add(`<p>` + htmlLink("packages-1.html", "Browse packages → files → functions") + ` · ` + htmlLink("findings-1.html", "Uncovered obligations and evidence requiring attention") + ` · ` + htmlLink("producers-1.html", "Evidence producers") + ` · ` + htmlLink("instrumentation-1.html", "Instrumentation") + `</p>`)
	s.findings = s.pages("findings", "Uncovered obligations and evidence requiring attention", "overview-1.html", `<p>Each finding identifies an existing metric, source range, and evidence state. Page search covers the current page. <a href="finding-filters-1.html">Browse by metric and status</a>.</p>`, "", "", htmlIndexRows)
	packages := s.pages("packages", "Packages", "overview-1.html", fmt.Sprintf("<p>%d packages. Open obligations are not covered plus evidence requiring attention.</p>", len(value.Packages)), "", "", htmlIndexRows)
	for _, pkg := range value.Packages {
		if s.err != nil {
			break
		}
		id := htmlID("package", pkg.Path)
		s.renderPackage(id, pkg)
		packages.add(`<article><h2>` + s.link(htmlPageName(id, 1), pkg.Path) + `</h2>` + s.status(htmlSummaryState(pkg.Summary)) + fmt.Sprintf("<p>%d open obligations · %d files</p>", htmlSummaryGaps(pkg.Summary), len(pkg.Files)) + s.metrics(pkg.Summary) + `</article>`)
	}
	packages.close()
	filters := s.pages("finding-filters", "Findings by metric and status", "findings-1.html", "", "", "", htmlIndexRows)
	// Fixed metric/state names; sort the map to keep byte-for-byte output stable.
	keys := make([]string, 0, len(s.findingGroups))
	for key := range s.findingGroups {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		p := s.findingGroups[key]
		p.close()
		filters.add(`<p>` + htmlLink(p.link(), escape(key)) + fmt.Sprintf(" · %d findings</p>", p.total))
	}
	filters.close()
	s.findings.close()
	overview.add(fmt.Sprintf(`<p>%d findings. %s</p>`, s.findingCount(), htmlLink(filters.link(), "Browse by metric and status")))
	overview.close()
	s.renderProducers(value)
	instrumentation := s.pages("instrumentation", "Instrumentation", "overview-1.html", `<p>Static entities requested versus instrumented.</p>`, `<div class="table"><table><thead><tr><th>Metric</th><th>Instrumented</th><th>Discovered</th><th>Unsupported</th><th>Unknown</th></tr></thead><tbody>`, `</tbody></table></div>`, htmlIndexRows)
	for _, m := range value.Instrumentation.Metrics {
		instrumentation.add(fmt.Sprintf("<tr><th>%s</th><td>%d</td><td>%d</td><td>%d</td><td>%d</td></tr>", s.field(string(m.Metric)), m.Coverage.Instrumented, m.Coverage.Discovered, m.Coverage.Unsupported, m.Coverage.Unknown))
	}
	instrumentation.close()
}
func (s *htmlDirectory) findingCount() int {
	n := 0
	for _, p := range s.findingGroups {
		n += p.total
	}
	return n
}
func (s *htmlDirectory) renderPackage(id string, pkg analysis.PackageReport) {
	p := s.pages(id, "Package files", "packages-1.html", s.paragraph("Package", pkg.Path)+s.paragraph("Status", pkg.Status)+fmt.Sprintf("<p>Evidence: %t</p>", pkg.Evidence)+s.metrics(pkg.Summary), "", "", htmlIndexRows)
	for _, file := range pkg.Files {
		if s.err != nil {
			break
		}
		fid := htmlID("file", pkg.Path, file.Path)
		s.renderFile(fid, p.link(), pkg.Path, file)
		p.add(`<article><h2>` + s.link(htmlPageName(fid, 1), file.Path) + `</h2>` + fmt.Sprintf("<p>%d functions</p>", len(file.Functions)) + s.metrics(file.Summary) + `</article>`)
	}
	p.close()
}
func (s *htmlDirectory) renderFile(id, parent, pkg string, file analysis.FileReport) {
	source := s.renderSource(pkg, file, id)
	intro := s.paragraph("File", file.Path) + s.paragraph("Package", pkg) + s.metrics(file.Summary) + `<p>` + htmlLink(source.first(), "Original source") + ` · ` + htmlLink(htmlPageName(id+"-annotations", 1), "Source annotations and mapping diagnostics") + `</p>`
	p := s.pages(id, "File functions", parent, intro, `<div class="table function-table"><table><thead><tr><th>Function</th>`, "", htmlIndexRows)
	for _, metric := range htmlMetrics(file.Summary) {
		p.before += "<th>" + metric.Short + "</th>"
	}
	p.before += `</tr></thead><tbody>`
	p.after = `</tbody></table></div>`
	for _, fn := range file.Functions {
		if s.err != nil {
			break
		}
		fid := htmlFunctionID(pkg, file.Path, fn)
		s.renderFunction(fid, p.link(), pkg, file.Path, fn, source)
		var row strings.Builder
		row.WriteString("<tr><th>" + s.link(htmlPageName(fid, 1), fn.Name) + "</th>")
		for _, metric := range htmlMetrics(fn.Summary) {
			fmt.Fprintf(&row, `<td class="coverage-cell"><b>%d/%d</b><small>%s · %s</small></td>`, metric.Summary.Covered, metric.Summary.Total, htmlPercentage(metric.Summary), htmlMetricStatus(metric.Summary))
		}
		row.WriteString("</tr>")
		p.add(row.String())
		// Reuse the obligation projection one function at a time. yield does not
		// retain findings or reanalyze the coverage model.
		input := analysis.Result{Packages: []analysis.PackageReport{{Path: pkg, Files: []analysis.FileReport{{Path: file.Path, Functions: []analysis.FunctionReport{fn}}}}}}
		walkHTMLFindings(input, func(f htmlFinding) {
			target := htmlPageName(fid, 1)
			if strings.HasPrefix(f.EvidenceAnchor, "decision-") {
				target = htmlPageName(f.EvidenceAnchor, 1)
				if f.ConditionIndex != nil {
					target = htmlPageName(htmlID("condition", f.EvidenceAnchor, strconv.Itoa(int(*f.ConditionIndex))), 1)
				}
			}
			body := `<article class="finding" data-status="` + escape(f.Status) + `"><h2>` + escape(f.Metric) + `: ` + s.field(f.Expression) + `</h2>` + s.paragraph("Package", pkg) + s.paragraph("Function", fn.Name) + `<p>` + s.link(source.location(f.Location), htmlLocationRange(f.Location)) + `</p>` + s.status(f.Status) + s.paragraph("Detail", f.Detail) + `<p>` + htmlLink(target, "Inspect evidence") + `</p></article>`
			s.findings.add(body)
			key := f.Metric + " / " + f.Status
			group := s.findingGroups[key]
			if group == nil {
				group = s.pages(htmlID("findings", key), key, "finding-filters-1.html", "", "", "", htmlIndexRows)
				s.findingGroups[key] = group
			}
			group.add(body)
		})
	}
	p.close()
}
func (s *htmlDirectory) renderFunction(id, parent, pkg, path string, fn analysis.FunctionReport, source htmlSourcePages) {
	intro := s.paragraph("Function", fn.Name) + s.metrics(fn.Summary)
	if fn.Location != nil {
		intro += `<p>` + s.link(source.location(*fn.Location), htmlLocationRange(*fn.Location)) + `</p>`
	}
	p := s.pages(id, "Function evidence", parent, intro, "", "", htmlIndexRows)
	for _, statement := range fn.Statements {
		p.add(`<article><h2>Statement</h2><p>` + s.link(source.location(statement.Location), htmlLocationRange(statement.Location)) + `</p>` + s.status(htmlMetricStatus(statement.Metric)) + fmt.Sprintf(`<p>Covered %d/%d · executions %d · statements %d</p></article>`, statement.Metric.Covered, statement.Metric.Total, statement.Count, statement.Statements))
	}
	for _, d := range fn.Decisions {
		if s.err != nil {
			break
		}
		did := htmlDecisionAnchor(pkg, d)
		s.renderDecision(did, p.link(), d, source)
		p.add(`<article><h2>Decision: ` + s.link(htmlPageName(did, 1), d.Expression) + `</h2>` + s.status(htmlMetricStatus(d.Summary.Decision)) + `<p>` + s.link(source.location(d.Location), htmlLocationRange(d.Location)) + `</p></article>`)
	}
	for _, c := range fn.Clauses {
		b := `<article><h2>` + s.field(string(c.Kind)) + ` ` + s.field(string(c.Role)) + ` clause</h2><p>` + s.link(source.location(c.Location), htmlLocationRange(c.Location)) + `</p>`
		b += s.paragraph("Clause ID", c.ClauseID) + s.status(htmlMetricStatus(c.BodyCoverage)) + fmt.Sprintf(`<p>Body: %d/%d · %s · executions %d</p>`, c.BodyCoverage.Covered, c.BodyCoverage.Total, htmlPercentage(c.BodyCoverage), c.BodyExecutions)
		b += s.status(htmlMetricStatus(c.SelectionCoverage)) + fmt.Sprintf(`<p>Selection: %d/%d · %s · direct selections %d</p>`, c.SelectionCoverage.Covered, c.SelectionCoverage.Total, htmlPercentage(c.SelectionCoverage), c.DirectSelections)
		p.add(b + `</article>`)
		for _, v := range c.SelectedAlternatives {
			p.add(fmt.Sprintf(`<p>Clause %s · selected alternative %d</p>`, s.field(c.ClauseID), v))
		}
	}
	for _, n := range fn.NoMatches {
		p.add(`<article><h2>No-match selection</h2>` + s.paragraph("Switch", n.SwitchID) + s.paragraph("Kind", string(n.Kind)) + `<p>` + s.link(source.location(n.Location), htmlLocationRange(n.Location)) + `</p>` + s.status(htmlMetricStatus(n.SelectionCoverage)) + fmt.Sprintf(`<p>%d/%d · %s</p></article>`, n.SelectionCoverage.Covered, n.SelectionCoverage.Total, htmlPercentage(n.SelectionCoverage)))
	}
	p.close()
}
func (s *htmlDirectory) renderDecision(id, parent string, d analysis.DecisionReport, source htmlSourcePages) {
	intro := s.paragraph("Decision", d.Expression) + s.paragraph("Decision ID", d.DecisionID) + s.sourceContext(source, d.Location) + fmt.Sprintf(`<p>true %t · false %t · not evaluated %d</p>`, d.DecisionCoverage.True, d.DecisionCoverage.False, d.NotEvaluated) + s.metrics(d.Summary)
	p := s.pages(id, "Decision and conditions", parent, intro, "", "", htmlIndexRows)
	for _, strategy := range []struct {
		name  string
		value analysis.MCDCAnalysisReport
	}{{"Unique-Cause MC/DC", d.MCDCUnique}, {"Masking MC/DC", d.MCDCMasking}} {
		m := strategy.value
		p.add(`<article><h2>` + strategy.name + `</h2>` + s.status(m.Status) + s.paragraph("Outcome / support / analysis", m.Outcome+" / "+m.Support+" / "+m.Analysis) + s.paragraph("Reason", m.Reason) + fmt.Sprintf("<p>Enabled %t · analyzed %d · aborted %d · invalid %d</p></article>", m.Enabled, m.EvaluationsAnalyzed, m.AbortedEvaluations, m.InvalidEvaluations))
	}
	for _, c := range d.Conditions {
		if s.err != nil {
			break
		}
		cid := htmlID("condition", id, strconv.Itoa(int(c.Index)))
		s.renderCondition(cid, p.link(), d, c, source)
		p.add(`<article class="condition-row"><h2>` + s.link(htmlPageName(cid, 1), fmt.Sprintf("Condition #%d: ", c.Index)+c.Expression) + `</h2>` + s.status(htmlMetricStatus(c.Metric)) + fmt.Sprintf(`<p>true %t · false %t · not evaluated %d</p>`, c.True, c.False, c.NotEvaluated) + s.paragraph("Unique-Cause", c.MCDCUnique.Status) + s.paragraph("Masking", c.MCDCMasking.Status) + `</article>`)
	}
	evaluations := s.renderEvaluations(id+"-evaluations", p.link(), d)
	p.add(fmt.Sprintf(`<p>%s · %d original observations, including repetitions.</p>`, htmlLink(evaluations, "Show evaluations"), len(d.Evaluations)))
	p.close()
}
func (s *htmlDirectory) renderCondition(id, parent string, d analysis.DecisionReport, c analysis.ConditionReport, source htmlSourcePages) {
	p := s.pages(id, "Condition evidence", parent, s.paragraph("Target condition", c.Expression)+fmt.Sprintf("<p>Condition #%d</p>", c.Index)+s.sourceContext(source, c.Location), "", "", htmlIndexRows)
	for _, strategy := range []struct {
		name, key string
		enabled   bool
		value     analysis.MCDCConditionReport
	}{{"Unique-Cause MC/DC", "unique", d.MCDCUnique.Enabled, c.MCDCUnique}, {"Masking MC/DC", "masking", d.MCDCMasking.Enabled, c.MCDCMasking}} {
		m := strategy.value
		b := `<section class="evidence"><h2>` + strategy.name + `</h2>` + s.status(m.Status) + s.paragraph("Outcome / support / analysis", m.Outcome+" / "+m.Support+" / "+m.Analysis) + s.paragraph("Detail", htmlMCDCDetail(m)) + s.paragraph("Reason", m.Reason)
		if strategy.enabled && m.Witness != nil {
			target := s.renderWitness(id+"-"+strategy.key, p.link(), strategy.name, d, c, m)
			b += `<p>` + htmlLink(target, "Compare Evaluation A / B") + `</p>`
		}
		p.add(b + `</section>`)
	}
	p.close()
}
func (s *htmlDirectory) provenance(label string, e analysis.EvaluationReport) string {
	result := evaluationOutcome(e)
	return `<div><h3>` + escape(label) + `</h3>` + s.paragraph("Evaluation", e.EvaluationID) + s.paragraph("Decision ID", e.DecisionID) + s.paragraph("Witness test", e.TestID) + s.paragraph("Status", e.Status) + s.paragraph("Decision result", result) + s.paragraph("Run", e.RunID) + s.paragraph("Package", e.PackagePath) + fmt.Sprintf("<p>process %d</p></div>", e.ProcessID)
}
func (s *htmlDirectory) renderWitness(id, parent, strategy string, d analysis.DecisionReport, c analysis.ConditionReport, m analysis.MCDCConditionReport) string {
	w := m.Witness
	intro := s.paragraph("Target condition", c.Expression) + fmt.Sprintf("<p>Target occurrence #%d · %s</p>", c.Index, s.status(m.Status)) + s.paragraph("Target observed A / B", htmlConditionValue(w.First.Conditions, c.Index)+" / "+htmlConditionValue(w.Second.Conditions, c.Index)) + s.paragraph("Target completion A / B", htmlConditionValue(w.FirstCompletion, c.Index)+" / "+htmlConditionValue(w.SecondCompletion, c.Index))
	intro += `<div class="decision-grid"><div class="mini-card"><h3>Evaluation A</h3>` + s.paragraph("Decision result", evaluationOutcome(w.First)) + s.paragraph("Witness test", w.First.TestID) + `</div><div class="mini-card"><h3>Evaluation B</h3>` + s.paragraph("Decision result", evaluationOutcome(w.Second)) + s.paragraph("Witness test", w.Second.TestID) + `</div></div>`
	if m.Reason != "" {
		intro += s.paragraph("Reason", m.Reason)
	}
	intro += `<details class="evidence"><summary>Run, package, process and evaluation provenance</summary><div class="decision-grid">` + s.provenance("Evaluation A (observed)", w.First) + s.provenance("Evaluation B (observed)", w.Second) + `</div></details><p>Completion values are analysis assignments, not additional observations.</p>`
	before := `<div class="table"><table class="witness-table"><caption>Independence witness</caption><thead><tr><th scope="col">Condition</th><th scope="col">Evaluation A (observed)</th><th scope="col">Evaluation B (observed)</th><th scope="col">Completion A (analysis)</th><th scope="col">Completion B (analysis)</th><th scope="col">Role</th></tr></thead><tbody>`
	p := s.pages(id, strategy+" witness", parent, intro, before, `</tbody></table></div>`, htmlIndexRows)
	for _, condition := range d.Conditions {
		role := ""
		if condition.Index == c.Index {
			role += "target "
		}
		if slices.Contains(w.MaskedConditions, condition.Index) {
			role += "masked "
		}
		if slices.Contains(w.UnobservedConditions, condition.Index) {
			role += "unobserved"
		}
		p.add(fmt.Sprintf(`<tr><th scope="row">#%d %s</th><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`, condition.Index, s.field(condition.Expression), s.field(htmlConditionValue(w.First.Conditions, condition.Index)), s.field(htmlConditionValue(w.Second.Conditions, condition.Index)), s.field(htmlConditionValue(w.FirstCompletion, condition.Index)), s.field(htmlConditionValue(w.SecondCompletion, condition.Index)), role))
	}
	p.close()
	return p.link()
}

// Split both observations and condition columns. A single decision with a very
// wide vector cannot produce an unbounded row or table header.
func (s *htmlDirectory) renderEvaluations(id, parent string, d analysis.DecisionReport) string {
	index := s.pages(id, "Observed evaluations", parent, fmt.Sprintf(`<p>%d original observations. Rows preserve their original order, including repetitions. All column groups refer to the same observation ordinals.</p>`, len(d.Evaluations)), "", "", htmlIndexRows)
	const columns = 16
	for start := 0; start < max(1, len(d.Conditions)); start += columns {
		if s.err != nil {
			break
		}
		end := min(start+columns, len(d.Conditions))
		group := fmt.Sprintf("%s-cols-%d", id, start)
		head := `<div class="table"><table><caption>Observed evaluations; provenance identifies run, package, and process</caption><thead><tr><th>Observation</th><th>Evaluation</th><th>Test</th><th>Status</th>`
		for _, c := range d.Conditions[start:end] {
			head += fmt.Sprintf("<th>#%d %s</th>", c.Index, s.field(c.Expression))
		}
		head += `<th>Decision result</th><th>Run</th><th>Package</th><th>Process</th></tr></thead><tbody>`
		p := s.pages(group, "Observed evaluations", index.link(), fmt.Sprintf("<p>Condition columns %d–%d of %d · %d observations</p>", min(start+1, end), end, len(d.Conditions), len(d.Evaluations)), head, `</tbody></table></div>`, htmlEvaluationRowsPerPage)
		for i, e := range d.Evaluations {
			if s.err != nil {
				break
			}
			var row strings.Builder
			fmt.Fprintf(&row, `<tr data-observation="%d"><th scope="row">%d</th><td>%s</td><td>%s</td><td>%s</td>`, i+1, i+1, s.field(e.EvaluationID), s.field(e.TestID), s.field(e.Status))
			for _, c := range d.Conditions[start:end] {
				row.WriteString("<td>" + s.field(htmlConditionValue(e.Conditions, c.Index)) + "</td>")
			}
			result := strconv.FormatBool(e.Result)
			if e.Status != "completed" {
				result = "not available (" + e.Status + ")"
			}
			row.WriteString("<td>" + s.field(result) + "</td><td>" + s.field(e.RunID) + "</td><td>" + s.field(e.PackagePath) + fmt.Sprintf("</td><td>%d</td></tr>", e.ProcessID))
			p.add(row.String())
		}
		p.close()
		index.add(fmt.Sprintf(`<p>%s · condition columns %d–%d of %d</p>`, htmlLink(p.link(), "Read observations"), min(start+1, end), end, len(d.Conditions)))
	}
	index.close()
	return index.link()
}
func (s *htmlDirectory) renderProducers(value analysis.Result) {
	p := s.pages("producers", "Evidence producers", "overview-1.html", `<p>Integrity, completeness, mapping and usability are independent of the run result.</p>`, "", "", htmlIndexRows)
	emit := func(outcomes []analysis.ProducerOutcome) {
		for _, o := range outcomes {
			p.add(`<article><h2>` + s.field(string(o.Producer)) + `</h2>` + s.paragraph("Integrity", string(o.Integrity)) + s.paragraph("Completeness", string(o.Completeness)) + s.paragraph("Mapping", string(o.Mapping)) + s.paragraph("Usability", string(o.Usability)) + `</article>`)
		}
	}
	emit(value.ProducerOutcomes)
	for _, run := range value.ContributingRuns {
		p.add(`<article><h2>Original measurement</h2>` + s.paragraph("Input", run.ID) + s.paragraph("Mode", string(run.MeasurementMode)) + s.paragraph("Run", string(run.Run.Status)) + s.paragraph("Evidence", completeness(run.Run.Complete)) + s.paragraph("Failure", string(run.Run.FailureKind)) + s.paragraph("Results", fmt.Sprintf("test %s · measurement %s · integrity %s · strict %s · threshold %s", run.Run.Results.Test, run.Run.Results.Measurement, run.Run.Results.Integrity, run.Run.Results.Strict, run.Run.Results.Threshold)) + `</article>`)
		emit(run.ProducerOutcomes)
		for _, e := range run.Errors {
			p.add(s.paragraph("Input", run.ID) + s.reportError(e))
		}
	}
	for _, e := range value.Errors {
		p.add(s.reportError(e))
	}
	p.close()
}
func (s *htmlDirectory) reportError(e analysis.ReportError) string {
	return `<article><h2>Diagnostic</h2>` + s.paragraph("Phase", e.Phase) + s.paragraph("Code", e.Code) + s.paragraph("Package", e.Package) + s.paragraph("Path", e.Path) + s.paragraph("Message", e.Message) + `</article>`
}

func evaluationOutcome(e analysis.EvaluationReport) string {
	if e.Status != "completed" {
		return "not available (" + e.Status + ")"
	}
	return strconv.FormatBool(e.Result)
}
