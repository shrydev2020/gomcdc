package report

import (
	"fmt"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

type htmlFunctionContext struct {
	packagePath, filePath, name string
}

func (s *htmlReportRenderer) functionContext(context htmlFunctionContext) string {
	return `<dl class="evidence-location">` +
		s.contextField("Package", context.packagePath) +
		s.contextField("File", context.filePath) +
		s.contextField("Function", context.name) + `</dl>`
}

func (s *htmlReportRenderer) decisionContext(context htmlFunctionContext, d analysis.DecisionReport, source htmlSourcePages) string {
	return s.functionContext(context) + `<dl class="evidence-location decision-location">` +
		s.contextField("Decision", d.Expression) + s.contextField("Decision ID", d.DecisionID) +
		`<div><dt>Decision location</dt><dd>` + s.sourceLocation(source, d.Location) + `</dd></div></dl>`
}

func (s *htmlReportRenderer) contextField(label, value string) string {
	return `<div><dt>` + escape(label) + `</dt><dd>` + s.output.field(value) + `</dd></div>`
}

func (s *htmlReportRenderer) mcdcRequirement(strategy string, c analysis.ConditionReport) string {
	var rule string
	switch strategy {
	case "unique":
		rule = "All other condition occurrences retain the same recorded value, including their short-circuit state."
	case "masking":
		rule = "Complete short-circuited values consistently with each observed evaluation. Flipping the target alone must change the Decision result in both completions. Each other condition either retains its value or is masked in both completions: flipping that condition alone preserves the Decision result."
	default:
		s.output.fail(fmt.Errorf("describe MC/DC requirement: unknown strategy %q", strategy))
		return ""
	}
	return `<section class="mcdc-requirement"><h3>Evidence to establish</h3><p>Two completed evaluations must observe target condition #` +
		fmt.Sprint(c.Index) + ` (` + s.output.field(c.Expression) + `) as false and true, with opposite Decision results.</p><p>` + rule + `</p></section>`
}

// targetObservations counts completed observations for the selected occurrence.
func (s *htmlReportRenderer) targetObservations(d analysis.DecisionReport, c analysis.ConditionReport) string {
	var combinations [2][2]int
	var shortCircuited, unavailable, incomplete int
	for _, e := range d.Evaluations {
		if e.Status != "completed" {
			incomplete++
			continue
		}
		result := 0
		if e.Result {
			result = 1
		}
		switch htmlConditionValue(e.Conditions, c.Index) {
		case "false":
			combinations[0][result]++
		case "true":
			combinations[1][result]++
		case "not-evaluated":
			shortCircuited++
		case "not available":
			unavailable++
		default:
			s.output.fail(fmt.Errorf("summarize decision %q evaluation %q condition #%d: invalid value %q", d.DecisionID, e.EvaluationID, c.Index, htmlConditionValue(e.Conditions, c.Index)))
			return ""
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="table"><table class="target-observations"><caption>Completed observations for target condition #%d</caption><thead><tr><th scope="col">Target value</th><th scope="col">Decision false</th><th scope="col">Decision true</th></tr></thead><tbody>`, c.Index)
	for target, counts := range combinations {
		fmt.Fprintf(&b, `<tr><th scope="row">%t</th><td>%d</td><td>%d</td></tr>`, target == 1, counts[0], counts[1])
	}
	b.WriteString(`</tbody></table></div>`)
	fmt.Fprintf(&b, `<p>Short-circuited target: %d · unavailable target value: %d · incomplete evaluations: %d.</p><p>An independence witness also depends on the other condition occurrences. Inspect the observations to compare their values and test provenance.</p>`, shortCircuited, unavailable, incomplete)
	return b.String()
}
