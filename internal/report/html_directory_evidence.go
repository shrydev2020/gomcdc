package report

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

func (s *htmlReportRenderer) renderDecision(id, parent string, context htmlFunctionContext, d analysis.DecisionReport, source htmlSourcePages) {
	if d.DecisionID == "" {
		s.output.fail(fmt.Errorf("render decision at %s: missing decision ID", htmlLocationRange(d.Location)))
		return
	}
	intro := s.decisionContext(context, d, source) + s.sourceContext(source, d.Location) + fmt.Sprintf(`<p>true %t · false %t · not evaluated %d</p>`, d.DecisionCoverage.True, d.DecisionCoverage.False, d.NotEvaluated) + s.metrics(d.Summary)
	p := s.output.pages(htmlSection{id: id, title: "Decision and conditions", parent: parent, intro: intro, rowLimit: htmlIndexRows})
	for _, strategy := range []struct {
		name  string
		value analysis.MCDCAnalysisReport
	}{{"Unique-Cause MC/DC", d.MCDCUnique}, {"Masking MC/DC", d.MCDCMasking}} {
		m := strategy.value
		p.add(`<article><h2>` + strategy.name + `</h2>` + s.output.status(m.Status) + s.output.paragraph("Outcome / support / analysis", m.Outcome+" / "+m.Support+" / "+m.Analysis) + s.output.paragraph("Reason", m.Reason) + fmt.Sprintf("<p>Enabled %t · analyzed %d · aborted %d · invalid %d</p></article>", m.Enabled, m.EvaluationsAnalyzed, m.AbortedEvaluations, m.InvalidEvaluations))
	}
	for column, c := range d.Conditions {
		if s.output.err != nil {
			break
		}
		cid := htmlID("condition", id, strconv.Itoa(int(c.Index)))
		s.renderCondition(cid, p.link(), context, d, c, column, source)
		p.add(`<article class="condition-row"><h2>` + s.output.link(htmlPageName(cid, 1), fmt.Sprintf("Condition #%d: ", c.Index)+c.Expression) + `</h2>` + s.output.status(htmlMetricStatus(c.Metric)) + fmt.Sprintf(`<p>true %t · false %t · not evaluated %d</p>`, c.True, c.False, c.NotEvaluated) + s.output.paragraph("Unique-Cause", c.MCDCUnique.Status) + s.output.paragraph("Masking", c.MCDCMasking.Status) + `</article>`)
	}
	evaluations := s.renderEvaluations(id+"-evaluations", p.link(), context, d, source)
	p.add(fmt.Sprintf(`<p>%s · %d original observations, including repetitions.</p>`, htmlLink(evaluations, "Show evaluations"), len(d.Evaluations)))
	p.close()
}
func (s *htmlReportRenderer) renderCondition(id, parent string, context htmlFunctionContext, d analysis.DecisionReport, c analysis.ConditionReport, column int, source htmlSourcePages) {
	p := s.output.pages(htmlSection{id: id, title: "Condition evidence", parent: parent, intro: s.decisionContext(context, d, source) + s.output.paragraph("Target condition", c.Expression) + fmt.Sprintf("<p>Condition #%d</p>", c.Index) + s.sourceContext(source, c.Location), rowLimit: htmlIndexRows})
	for _, strategy := range []struct {
		name, key string
		enabled   bool
		value     analysis.MCDCConditionReport
	}{{"Unique-Cause MC/DC", "unique", d.MCDCUnique.Enabled, c.MCDCUnique}, {"Masking MC/DC", "masking", d.MCDCMasking.Enabled, c.MCDCMasking}} {
		m := strategy.value
		b := `<section class="evidence"><h2>` + strategy.name + `</h2>` + s.output.status(m.Status) + s.output.paragraph("Outcome / support / analysis", m.Outcome+" / "+m.Support+" / "+m.Analysis) + s.output.paragraph("Detail", htmlMCDCStatusDetail(m)) + s.output.paragraph("Reason", m.Reason)
		if strategy.enabled && m.Status == "not-covered" {
			b += s.mcdcRequirement(strategy.key, c)
		}
		if strategy.enabled && m.Witness != nil {
			target := s.renderWitness(id+"-"+strategy.key, p.link(), strategy.name, context, d, c, m, source)
			b += `<p>` + htmlLink(target, "Compare Evaluation A / B") + `</p>`
		}
		p.add(b + `</section>`)
	}
	p.add(s.targetObservations(d, c))
	observationPage := htmlPageName(htmlDecisionAnchor(context.packagePath, d)+"-evaluations-cols-"+strconv.Itoa(column/htmlEvaluationColumns*htmlEvaluationColumns), 1)
	p.add(`<p>` + htmlLink(observationPage, "Inspect observations for target condition #"+strconv.Itoa(int(c.Index))) + `</p>`)
	p.close()
}
func (s *htmlReportRenderer) provenance(label string, e analysis.EvaluationReport) string {
	result := evaluationOutcome(e)
	return `<div><h3>` + escape(label) + `</h3>` + s.output.paragraph("Evaluation", e.EvaluationID) + s.output.paragraph("Decision ID", e.DecisionID) + s.output.paragraph("Witness test", e.TestID) + s.output.paragraph("Status", e.Status) + s.output.paragraph("Decision result", result) + s.output.paragraph("Run", e.RunID) + s.output.paragraph("Package", e.PackagePath) + fmt.Sprintf("<p>process %d</p></div>", e.ProcessID)
}
func (s *htmlReportRenderer) renderWitness(id, parent, strategy string, context htmlFunctionContext, d analysis.DecisionReport, c analysis.ConditionReport, m analysis.MCDCConditionReport, source htmlSourcePages) string {
	w := m.Witness
	intro := s.decisionContext(context, d, source) + s.output.paragraph("Target condition", c.Expression) + fmt.Sprintf("<p>Target occurrence #%d · %s</p>", c.Index, s.output.status(m.Status)) + s.output.paragraph("Target observed A / B", htmlConditionValue(w.First.Conditions, c.Index)+" / "+htmlConditionValue(w.Second.Conditions, c.Index)) + s.output.paragraph("Target completion A / B", htmlConditionValue(w.FirstCompletion, c.Index)+" / "+htmlConditionValue(w.SecondCompletion, c.Index))
	intro += `<div class="decision-grid"><div class="mini-card"><h3>Evaluation A</h3>` + s.output.paragraph("Decision result", evaluationOutcome(w.First)) + s.output.paragraph("Witness test", w.First.TestID) + `</div><div class="mini-card"><h3>Evaluation B</h3>` + s.output.paragraph("Decision result", evaluationOutcome(w.Second)) + s.output.paragraph("Witness test", w.Second.TestID) + `</div></div>`
	if m.Reason != "" {
		intro += s.output.paragraph("Reason", m.Reason)
	}
	intro += `<details class="evidence"><summary>Run, package, process and evaluation provenance</summary><div class="decision-grid">` + s.provenance("Evaluation A (observed)", w.First) + s.provenance("Evaluation B (observed)", w.Second) + `</div></details><p>Completion columns show the analysis assignments used to establish independence.</p>`
	before := `<div class="table"><table class="witness-table"><caption>Independence witness</caption><thead><tr><th scope="col">Condition</th><th scope="col">Evaluation A (observed)</th><th scope="col">Evaluation B (observed)</th><th scope="col">Completion A (analysis)</th><th scope="col">Completion B (analysis)</th><th scope="col">Role</th></tr></thead><tbody>`
	p := s.output.pages(htmlSection{id: id, title: strategy + " witness", parent: parent, intro: intro, before: before, after: `</tbody></table></div>`, rowLimit: htmlIndexRows})
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
		p.add(fmt.Sprintf(`<tr><th scope="row">#%d %s</th><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`, condition.Index, s.output.field(condition.Expression), s.output.field(htmlConditionValue(w.First.Conditions, condition.Index)), s.output.field(htmlConditionValue(w.Second.Conditions, condition.Index)), s.output.field(htmlConditionValue(w.FirstCompletion, condition.Index)), s.output.field(htmlConditionValue(w.SecondCompletion, condition.Index)), role))
	}
	p.close()
	return p.link()
}

// renderEvaluations paginates rows and groups condition columns by occurrence.
func (s *htmlReportRenderer) renderEvaluations(id, parent string, context htmlFunctionContext, d analysis.DecisionReport, source htmlSourcePages) string {
	index := s.output.pages(htmlSection{id: id, title: "Observed evaluations", parent: parent, intro: s.decisionContext(context, d, source) + fmt.Sprintf(`<p>%d original observations. Rows preserve their original order, including repetitions. All column groups refer to the same observation ordinals.</p>`, len(d.Evaluations)), rowLimit: htmlIndexRows})
	for start := 0; start < max(1, len(d.Conditions)); start += htmlEvaluationColumns {
		if s.output.err != nil {
			break
		}
		end := min(start+htmlEvaluationColumns, len(d.Conditions))
		group := fmt.Sprintf("%s-cols-%d", id, start)
		head := `<div class="table"><table><caption>Observed evaluations; provenance identifies run, package, and process</caption><thead><tr><th>Observation</th><th>Evaluation</th><th>Test</th><th>Status</th>`
		for _, c := range d.Conditions[start:end] {
			head += fmt.Sprintf("<th>#%d %s</th>", c.Index, s.output.field(c.Expression))
		}
		head += `<th>Decision result</th><th>Run</th><th>Package</th><th>Process</th></tr></thead><tbody>`
		p := s.output.pages(htmlSection{id: group, title: "Observed evaluations", parent: index.link(), intro: s.decisionContext(context, d, source) + fmt.Sprintf("<p>Condition columns %d–%d of %d · %d observations</p>", min(start+1, end), end, len(d.Conditions), len(d.Evaluations)), before: head, after: `</tbody></table></div>`, rowLimit: htmlEvaluationRowsPerPage})
		for i, e := range d.Evaluations {
			if s.output.err != nil {
				break
			}
			var row strings.Builder
			fmt.Fprintf(&row, `<tr data-observation="%d"><th scope="row">%d</th><td>%s</td><td>%s</td><td>%s</td>`, i+1, i+1, s.output.field(e.EvaluationID), s.output.field(e.TestID), s.output.field(e.Status))
			for _, c := range d.Conditions[start:end] {
				row.WriteString("<td>" + s.output.field(htmlConditionValue(e.Conditions, c.Index)) + "</td>")
			}
			result := evaluationOutcome(e)
			row.WriteString("<td>" + s.output.field(result) + "</td><td>" + s.output.field(e.RunID) + "</td><td>" + s.output.field(e.PackagePath) + fmt.Sprintf("</td><td>%d</td></tr>", e.ProcessID))
			p.add(row.String())
		}
		p.close()
		index.add(fmt.Sprintf(`<p>%s · condition columns %d–%d of %d</p>`, htmlLink(p.link(), "Read observations"), min(start+1, end), end, len(d.Conditions)))
	}
	index.close()
	return index.link()
}
func evaluationOutcome(e analysis.EvaluationReport) string {
	if e.Status != "completed" {
		return "not available (" + e.Status + ")"
	}
	return strconv.FormatBool(e.Result)
}

func htmlDecisionAnchor(pkg string, decision analysis.DecisionReport) string {
	return fmt.Sprintf("decision-%x", sha256.Sum256([]byte(pkg+"\x00"+decision.DecisionID)))
}
