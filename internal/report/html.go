package report

import (
	"fmt"
	"html/template"
	"io"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

type htmlMetric struct {
	Name, Short string
	Summary     MetricSummary
}

// WriteHTML writes one self-contained static HTML report.
func WriteHTML(w io.Writer, input Input) error {
	return writeHTMLReport(w, Build(input))
}

func writeHTMLReport(w io.Writer, value Report) error {
	t, err := template.New("report").Funcs(template.FuncMap{
		"metrics": htmlMetrics, "pct": htmlPercentage, "class": htmlMetricClass,
		"loc": htmlLocation, "complete": completeness,
	}).Parse(htmlDocument)
	if err != nil {
		return fmt.Errorf("parse HTML report template: %w", err)
	}
	if err := t.Execute(w, value); err != nil {
		return fmt.Errorf("render HTML report: %w", err)
	}
	return nil
}

func htmlMetrics(s Summary) []htmlMetric {
	return []htmlMetric{
		{"Statement", "Stmt", s.Statement}, {"Function", "Func", s.Function}, {"Decision", "Decision", s.Decision},
		{"Switch clause body", "Sw body", s.SwitchClauseBody}, {"Type switch clause body", "Type body", s.TypeSwitchClauseBody},
		{"Select clause body", "Select body", s.SelectClauseBody}, {"Switch clause selection", "Sw select", s.SwitchClauseSelection},
		{"Type switch clause selection", "Type select", s.TypeSwitchClauseSelection}, {"Condition", "Condition", s.Condition},
		{"Unique-Cause MC/DC", "UC MC/DC", s.MCDCUnique}, {"Masking MC/DC", "Mask MC/DC", s.MCDCMasking},
	}
}
func htmlPercentage(m MetricSummary) string {
	if m.Percentage == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", *m.Percentage)
}
func htmlMetricClass(m MetricSummary) string {
	switch {
	case !m.Enabled:
		return "disabled"
	case m.Unknown > 0 || m.Unsupported > 0:
		return "attention"
	case m.Total == 0:
		return "empty"
	case m.Covered == m.Total:
		return "covered"
	case m.Covered == 0:
		return "uncovered"
	default:
		return "partial"
	}
}
func htmlLocation(l cover.SourceLocation) string {
	return fmt.Sprintf("%s:%d:%d", l.File, l.Start.Line, l.Start.Column)
}

const htmlDocument = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Module}} - gomcdc coverage</title><style>
:root{color-scheme:light dark;--bg:#f6f8fa;--panel:#fff;--text:#1f2328;--muted:#59636e;--line:#d1d9e0;--link:#0969da;--good:#dafbe1;--warn:#fff8c5;--bad:#ffebe9;--info:#fbefff}
@media(prefers-color-scheme:dark){:root{--bg:#0d1117;--panel:#161b22;--text:#e6edf3;--muted:#8d96a0;--line:#30363d;--link:#58a6ff;--good:#12261e;--warn:#2d250f;--bad:#2d1517;--info:#23173d}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 system-ui,sans-serif}a{color:var(--link);text-decoration:none}code,.loc{font-family:ui-monospace,monospace}.layout{display:grid;grid-template-columns:270px minmax(0,1fr);min-height:100vh}.side{position:sticky;top:0;height:100vh;overflow:auto;background:var(--panel);border-right:1px solid var(--line);padding:1.2rem}.brand{font-size:1.15rem;font-weight:700}.module,.muted,.loc{color:var(--muted)}.module{overflow-wrap:anywhere;margin:.3rem 0 1rem}.nav{list-style:none;padding:0}.nav a{display:block;padding:.35rem}.main{min-width:0;max-width:1600px;padding:2rem}.panel{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:1rem;margin-bottom:1rem;scroll-margin-top:1rem}h1,h2,h3,h4{margin:.1rem 0 .6rem}.meta,.head{display:flex;gap:1rem;flex-wrap:wrap}.head{justify-content:space-between}.notice{padding:.7rem;background:var(--warn)}.metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(135px,1fr));gap:.6rem;margin:.8rem 0}.metric{border:1px solid var(--line);border-radius:7px;padding:.6rem}.metric .name{color:var(--muted);font-size:.78rem;min-height:2.2em}.value{font-size:1.1rem;font-weight:700}.covered{background:var(--good)}.partial{background:var(--warn)}.uncovered{background:var(--bad)}.attention{background:var(--info)}.disabled,.empty{opacity:.65}.file{border-top:1px solid var(--line);margin-top:1rem;padding-top:1rem}.fn{border:1px solid var(--line);border-radius:8px;margin:.7rem 0;padding:.8rem}.table{overflow:auto}table{width:100%;border-collapse:collapse}th,td{border-bottom:1px solid var(--line);padding:.45rem;text-align:left;vertical-align:top}th{font-size:.75rem;color:var(--muted)}td.num{text-align:right;white-space:nowrap}details{border-top:1px solid var(--line);padding:.55rem 0}summary{cursor:pointer;font-weight:600}.expr{display:block;background:var(--bg);padding:.5rem;margin:.4rem 0;overflow:auto}.tag{border:1px solid var(--line);border-radius:99px;padding:.05rem .4rem}.emptymsg{text-align:center;color:var(--muted);padding:1rem}.footer{color:var(--muted);margin-top:2rem}
@media(max-width:800px){.layout{display:block}.side{position:static;height:auto}.main{padding:1rem}.nav{display:flex;overflow:auto}.nav a{white-space:nowrap}}
</style></head><body><div class="layout">
<aside class="side" aria-label="Package navigation"><div class="brand">gomcdc</div><div class="module">{{.Module}}</div><b>Overview</b><ul class="nav"><li><a href="#summary">Module summary</a></li><li><a href="#instrumentation">Instrumentation</a></li></ul><b>Packages</b><ul class="nav">{{range $pi,$pkg:=.Packages}}<li><a href="#pkg-{{$pi}}">{{$pkg.Path}}</a></li>{{else}}<li>No packages</li>{{end}}</ul></aside>
<main class="main"><header class="panel" id="summary"><h1>{{.Module}}</h1><div class="meta muted"><span>Report {{.Version}}</span><span>Run: {{.Run.Status}}</span><span>{{complete .Run.Complete}}</span><span>Mode: {{.MeasurementMode}}</span></div>{{if not .Run.Complete}}<p class="notice" role="status"><b>Partial report.</b> Coverage reflects only validated evidence.</p>{{end}}<div class="metrics">{{range metrics .Summary}}<div class="metric {{class .Summary}}"><div class="name">{{.Name}}</div><div class="value">{{pct .Summary}}</div><div>{{.Summary.Covered}} / {{.Summary.Total}}</div><small>unsupported {{.Summary.Unsupported}} | unknown {{.Summary.Unknown}} | possibly infeasible {{.Summary.PossiblyInfeasible}}</small></div>{{end}}</div></header>
<section class="panel" id="instrumentation"><h2>Instrumentation</h2><div class="table"><table><thead><tr><th>Metric</th><th>Instrumented</th><th>Discovered</th><th>Unsupported</th><th>Unknown</th></tr></thead><tbody>{{range .Instrumentation.Metrics}}<tr><td>{{.Metric}}</td><td class="num">{{.Coverage.Instrumented}}</td><td class="num">{{.Coverage.Discovered}}</td><td class="num">{{.Coverage.Unsupported}}</td><td class="num">{{.Coverage.Unknown}}</td></tr>{{end}}</tbody></table></div></section>
<section><h2>Packages</h2>{{range $pi,$pkg:=.Packages}}<article class="panel" id="pkg-{{$pi}}"><div class="head"><h3>{{$pkg.Path}}</h3><span class="muted">{{$pkg.Status}} | evidence {{$pkg.Evidence}}</span></div><div class="metrics">{{range metrics $pkg.Summary}}<div class="metric {{class .Summary}}"><div class="name">{{.Short}}</div><div class="value">{{pct .Summary}}</div><div>{{.Summary.Covered}} / {{.Summary.Total}}</div><small>unsupported {{.Summary.Unsupported}} | unknown {{.Summary.Unknown}} | possibly infeasible {{.Summary.PossiblyInfeasible}}</small></div>{{end}}</div>
{{range $fi,$file:=$pkg.Files}}<section class="file" id="pkg-{{$pi}}-file-{{$fi}}"><h3><span class="tag">file</span> {{$file.Path}}</h3><div class="table"><table><thead><tr><th>Function</th>{{range metrics $file.Summary}}<th title="{{.Name}}">{{.Short}}</th>{{end}}</tr></thead><tbody>{{range $fni,$fn:=$file.Functions}}<tr><td><a href="#pkg-{{$pi}}-file-{{$fi}}-fn-{{$fni}}">{{$fn.Name}}</a>{{if $fn.Location}}<div class="loc">{{loc $fn.Location}}</div>{{end}}</td>{{range metrics $fn.Summary}}<td class="num">{{.Summary.Covered}}/{{.Summary.Total}}<br><small>{{pct .Summary}}</small></td>{{end}}</tr>{{end}}</tbody></table></div>
{{range $fni,$fn:=$file.Functions}}<article class="fn" id="pkg-{{$pi}}-file-{{$fi}}-fn-{{$fni}}"><div class="head"><h4>{{$fn.Name}}</h4>{{if $fn.Location}}<a class="loc" href="#pkg-{{$pi}}-file-{{$fi}}">{{loc $fn.Location}}</a>{{end}}</div>{{range $fn.Decisions}}<details><summary>Decision <code>{{.Expression}}</code> <span class="muted">{{loc .Location}}</span></summary><code class="expr">{{.Expression}}</code><div>Outcomes: true {{.DecisionCoverage.True}} | false {{.DecisionCoverage.False}} | skipped {{.NotEvaluated}}</div><ul>{{range .Conditions}}<li><code>{{.Expression}}</code> <span class="loc">{{loc .Location}}</span><br>true {{.True}} | false {{.False}} | skipped {{.NotEvaluated}} | Unique {{.MCDCUnique.Status}} | Masking {{.MCDCMasking.Status}}{{with .MCDCUnique.Witness}}<div>Unique witness: {{.First.Conditions}} -&gt; {{.First.Result}} / {{.Second.Conditions}} -&gt; {{.Second.Result}}</div>{{end}}{{with .MCDCMasking.Witness}}<div>Masking witness: {{.First.Conditions}} -&gt; {{.First.Result}} / {{.Second.Conditions}} -&gt; {{.Second.Result}}; completions {{.FirstCompletion}} / {{.SecondCompletion}}</div>{{end}}</li>{{end}}</ul></details>{{end}}{{range $fn.Clauses}}<details><summary>{{.Kind}} {{.Role}} clause <span class="muted">{{loc .Location}}</span></summary><div>Body: {{.BodyCoverage.Covered}}/{{.BodyCoverage.Total}} ({{pct .BodyCoverage}}), executions {{.BodyExecutions}}</div><div>Selection: {{.SelectionCoverage.Covered}}/{{.SelectionCoverage.Total}} ({{pct .SelectionCoverage}})</div></details>{{end}}{{if and (not $fn.Decisions) (not $fn.Clauses)}}<div class="emptymsg">No decision or clause detail.</div>{{end}}</article>{{end}}</section>{{end}}</article>{{else}}<div class="panel emptymsg">No packages were discovered.</div>{{end}}</section>
<footer class="footer">Generated by gomcdc. Self-contained; no network requests or JavaScript.</footer></main></div></body></html>
`
