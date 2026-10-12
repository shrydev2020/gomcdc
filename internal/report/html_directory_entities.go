package report

import (
	"fmt"
	"maps"
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
func (s *htmlReportRenderer) render(value analysis.Result) {
	overview := s.output.pages(htmlSection{id: "overview", title: "Coverage overview", rowLimit: htmlIndexRows})
	overview.add(s.output.paragraph("Module", value.Module) + s.output.paragraph("Run status", string(value.Run.Status)) + s.output.paragraph("Evidence", completeness(value.Run.Complete)) + s.output.paragraph("Measurement", string(value.MeasurementMode)))
	overview.add(s.output.paragraph("Run results", fmt.Sprintf("test %s · measurement %s · integrity %s · strict %s · threshold %s", value.Run.Results.Test, value.Run.Results.Measurement, value.Run.Results.Integrity, value.Run.Results.Strict, value.Run.Results.Threshold)))
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
	s.findings = s.output.pages(htmlSection{id: "findings", title: "Uncovered obligations and evidence requiring attention", parent: "overview-1.html", intro: `<p>Each finding identifies an existing metric, source range, and evidence state. Page search covers the current page. <a href="finding-filters-1.html">Browse by metric and status</a>.</p>`, rowLimit: htmlIndexRows})
	packages := s.output.pages(htmlSection{id: "packages", title: "Packages", parent: "overview-1.html", intro: fmt.Sprintf("<p>%d packages. Open obligations are not covered plus evidence requiring attention.</p>", len(value.Packages)), rowLimit: htmlIndexRows})
	for _, pkg := range value.Packages {
		if s.output.err != nil {
			break
		}
		id := htmlID("package", pkg.Path)
		s.renderPackage(id, pkg)
		packages.add(`<article><h2>` + s.output.link(htmlPageName(id, 1), pkg.Path) + `</h2>` + s.output.status(htmlSummaryState(pkg.Summary)) + fmt.Sprintf("<p>%d open obligations · %d files</p>", htmlSummaryGaps(pkg.Summary), len(pkg.Files)) + s.metrics(pkg.Summary) + `</article>`)
	}
	packages.close()
	filters := s.output.pages(htmlSection{id: "finding-filters", title: "Findings by metric and status", parent: "findings-1.html", rowLimit: htmlIndexRows})
	// Fixed metric/state names; sort the map to keep byte-for-byte output stable.
	keys := slices.Sorted(maps.Keys(s.findingGroups))
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
	instrumentation := s.output.pages(htmlSection{id: "instrumentation", title: "Instrumentation", parent: "overview-1.html", intro: `<p>Static entities requested versus instrumented.</p>`, before: `<div class="table"><table><thead><tr><th>Metric</th><th>Instrumented</th><th>Discovered</th><th>Unsupported</th><th>Unknown</th></tr></thead><tbody>`, after: `</tbody></table></div>`, rowLimit: htmlIndexRows})
	for _, m := range value.Instrumentation.Metrics {
		instrumentation.add(fmt.Sprintf("<tr><th>%s</th><td>%d</td><td>%d</td><td>%d</td><td>%d</td></tr>", s.output.field(string(m.Metric)), m.Coverage.Instrumented, m.Coverage.Discovered, m.Coverage.Unsupported, m.Coverage.Unknown))
	}
	instrumentation.close()
}
func (s *htmlReportRenderer) findingCount() int {
	n := 0
	for _, p := range s.findingGroups {
		n += p.total
	}
	return n
}
func (s *htmlReportRenderer) renderPackage(id string, pkg analysis.PackageReport) {
	p := s.output.pages(htmlSection{id: id, title: "Package files", parent: "packages-1.html", intro: s.output.paragraph("Package", pkg.Path) + s.output.paragraph("Status", pkg.Status) + fmt.Sprintf("<p>Evidence: %t</p>", pkg.Evidence) + s.metrics(pkg.Summary), rowLimit: htmlIndexRows})
	for _, file := range pkg.Files {
		if s.output.err != nil {
			break
		}
		fid := htmlID("file", pkg.Path, file.Path)
		s.renderFile(fid, p.link(), pkg.Path, file)
		p.add(`<article><h2>` + s.output.link(htmlPageName(fid, 1), file.Path) + `</h2>` + fmt.Sprintf("<p>%d functions</p>", len(file.Functions)) + s.metrics(file.Summary) + `</article>`)
	}
	p.close()
}
func (s *htmlReportRenderer) renderFile(id, parent, pkg string, file analysis.FileReport) {
	source := s.renderSource(pkg, file, id)
	intro := s.output.paragraph("File", file.Path) + s.output.paragraph("Package", pkg) + s.metrics(file.Summary) + `<p>` + htmlLink(source.first(), "Original source") + ` · ` + htmlLink(htmlPageName(id+"-annotations", 1), "Source annotations and mapping diagnostics") + `</p>`
	p := s.output.pages(htmlSection{id: id, title: "File functions", parent: parent, intro: intro, before: `<div class="table function-table"><table><thead><tr><th>Function</th>`, rowLimit: htmlIndexRows})
	for _, metric := range htmlMetrics(file.Summary) {
		p.before += "<th>" + metric.Short + "</th>"
	}
	p.before += `</tr></thead><tbody>`
	p.after = `</tbody></table></div>`
	for _, fn := range file.Functions {
		if s.output.err != nil {
			break
		}
		fid := htmlFunctionID(pkg, file.Path, fn)
		s.renderFunction(fid, p.link(), pkg, file.Path, fn, source)
		var row strings.Builder
		row.WriteString("<tr><th>" + s.output.link(htmlPageName(fid, 1), fn.Name) + "</th>")
		for _, metric := range htmlMetrics(fn.Summary) {
			fmt.Fprintf(&row, `<td class="coverage-cell"><b>%d/%d</b><small>%s · %s</small></td>`, metric.Summary.Covered, metric.Summary.Total, htmlPercentage(metric.Summary), htmlMetricStatus(metric.Summary))
		}
		row.WriteString("</tr>")
		p.add(row.String())
		// Append findings while this function's source links are available.
		walkFunctionHTMLFindings(pkg, file.Path, fn, fid, func(f htmlFinding) {
			target := htmlPageName(fid, 1)
			if f.DecisionID != "" {
				target = htmlPageName(f.EvidenceAnchor, 1)
				if f.ConditionIndex != nil {
					target = htmlPageName(htmlID("condition", f.EvidenceAnchor, strconv.Itoa(int(*f.ConditionIndex))), 1)
				}
			}
			body := `<article class="finding" data-status="` + escape(f.Status) + `"><h2>` + escape(f.Metric) + `: ` + s.output.field(f.Expression) + `</h2>` + s.output.paragraph("Package", pkg) + s.output.paragraph("Function", fn.Name) + `<p>` + s.sourceLocation(source, f.Location) + `</p>` + s.output.status(f.Status) + s.output.paragraph("Detail", f.Detail) + `<p>` + htmlLink(target, "Inspect evidence") + `</p></article>`
			s.findings.add(body)
			key := f.Metric + " / " + f.Status
			group := s.findingGroups[key]
			if group == nil {
				group = s.output.pages(htmlSection{id: htmlID("findings", key), title: key, parent: "finding-filters-1.html", rowLimit: htmlIndexRows})
				s.findingGroups[key] = group
			}
			group.add(body)
		})
	}
	p.close()
}
func (s *htmlReportRenderer) renderFunction(id, parent, pkg, path string, fn analysis.FunctionReport, source htmlSourcePages) {
	intro := s.output.paragraph("Function", fn.Name) + s.metrics(fn.Summary)
	if fn.Location != nil {
		intro += `<p>` + s.sourceLocation(source, *fn.Location) + `</p>`
	}
	p := s.output.pages(htmlSection{id: id, title: "Function evidence", parent: parent, intro: intro, rowLimit: htmlIndexRows})
	for _, statement := range fn.Statements {
		p.add(`<article><h2>Statement</h2><p>` + s.sourceLocation(source, statement.Location) + `</p>` + s.output.status(htmlMetricStatus(statement.Metric)) + fmt.Sprintf(`<p>Covered %d/%d · executions %d · statements %d</p></article>`, statement.Metric.Covered, statement.Metric.Total, statement.Count, statement.Statements))
	}
	for _, d := range fn.Decisions {
		if s.output.err != nil {
			break
		}
		did := htmlDecisionAnchor(pkg, d)
		s.renderDecision(did, p.link(), d, source)
		p.add(`<article><h2>Decision: ` + s.output.link(htmlPageName(did, 1), d.Expression) + `</h2>` + s.output.status(htmlMetricStatus(d.Summary.Decision)) + `<p>` + s.sourceLocation(source, d.Location) + `</p></article>`)
	}
	for _, c := range fn.Clauses {
		b := `<article><h2>` + s.output.field(string(c.Kind)) + ` ` + s.output.field(string(c.Role)) + ` clause</h2><p>` + s.sourceLocation(source, c.Location) + `</p>`
		b += s.output.paragraph("Clause ID", c.ClauseID) + s.output.status(htmlMetricStatus(c.BodyCoverage)) + fmt.Sprintf(`<p>Body: %d/%d · %s · executions %d</p>`, c.BodyCoverage.Covered, c.BodyCoverage.Total, htmlPercentage(c.BodyCoverage), c.BodyExecutions)
		b += s.output.status(htmlMetricStatus(c.SelectionCoverage)) + fmt.Sprintf(`<p>Selection: %d/%d · %s · direct selections %d</p>`, c.SelectionCoverage.Covered, c.SelectionCoverage.Total, htmlPercentage(c.SelectionCoverage), c.DirectSelections)
		p.add(b + `</article>`)
		for _, v := range c.SelectedAlternatives {
			p.add(fmt.Sprintf(`<p>Clause %s · selected alternative %d</p>`, s.output.field(c.ClauseID), v))
		}
	}
	for _, n := range fn.NoMatches {
		p.add(`<article><h2>No-match selection</h2>` + s.output.paragraph("Switch", n.SwitchID) + s.output.paragraph("Kind", string(n.Kind)) + `<p>` + s.sourceLocation(source, n.Location) + `</p>` + s.output.status(htmlMetricStatus(n.SelectionCoverage)) + fmt.Sprintf(`<p>%d/%d · %s</p></article>`, n.SelectionCoverage.Covered, n.SelectionCoverage.Total, htmlPercentage(n.SelectionCoverage)))
	}
	p.close()
}
func (s *htmlReportRenderer) renderProducers(value analysis.Result) {
	p := s.output.pages(htmlSection{id: "producers", title: "Evidence producers", parent: "overview-1.html", intro: `<p>Integrity, completeness, mapping and usability are independent of the run result.</p>`, rowLimit: htmlIndexRows})
	emit := func(outcomes []analysis.ProducerOutcome) {
		for _, o := range outcomes {
			p.add(`<article><h2>` + s.output.field(string(o.Producer)) + `</h2>` + s.output.paragraph("Integrity", string(o.Integrity)) + s.output.paragraph("Completeness", string(o.Completeness)) + s.output.paragraph("Mapping", string(o.Mapping)) + s.output.paragraph("Usability", string(o.Usability)) + `</article>`)
		}
	}
	emit(value.ProducerOutcomes)
	for _, run := range value.ContributingRuns {
		p.add(`<article><h2>Original measurement</h2>` + s.output.paragraph("Input", run.ID) + s.output.paragraph("Mode", string(run.MeasurementMode)) + s.output.paragraph("Run", string(run.Run.Status)) + s.output.paragraph("Evidence", completeness(run.Run.Complete)) + s.output.paragraph("Failure", string(run.Run.FailureKind)) + s.output.paragraph("Results", fmt.Sprintf("test %s · measurement %s · integrity %s · strict %s · threshold %s", run.Run.Results.Test, run.Run.Results.Measurement, run.Run.Results.Integrity, run.Run.Results.Strict, run.Run.Results.Threshold)) + `</article>`)
		emit(run.ProducerOutcomes)
		for _, e := range run.Errors {
			p.add(s.output.paragraph("Input", run.ID) + s.reportError(e))
		}
	}
	for _, e := range value.Errors {
		p.add(s.reportError(e))
	}
	p.close()
}
func (s *htmlReportRenderer) reportError(e analysis.ReportError) string {
	return `<article><h2>Diagnostic</h2>` + s.output.paragraph("Phase", e.Phase) + s.output.paragraph("Code", e.Code) + s.output.paragraph("Package", e.Package) + s.output.paragraph("Path", e.Path) + s.output.paragraph("Message", e.Message) + `</article>`
}
