package report

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

type htmlSourceRange struct{ start, end, page, firstLine, lastLine int }
type htmlSourcePages struct {
	id      string
	source  []byte
	ranges  []htmlSourceRange
	anchors []int
}

func (v htmlSourcePages) first() string { return htmlPageName(v.id, 1) }
func (v htmlSourcePages) offset(offset int) (string, error) {
	// Ranges partition the source; each anchor starts a rendered fragment.
	i, found := slices.BinarySearchFunc(v.ranges, offset, func(r htmlSourceRange, n int) int {
		if n < r.start {
			return 1
		}
		if n >= r.end {
			return -1
		}
		return 0
	})
	if !found {
		return "", fmt.Errorf("source %q has no page for byte %d", v.id, offset)
	}
	a, exact := slices.BinarySearch(v.anchors, offset)
	if !exact {
		a--
	}
	if a < 0 || v.anchors[a] < v.ranges[i].start {
		return "", fmt.Errorf("source %q page %d has no anchor for byte %d", v.id, v.ranges[i].page, offset)
	}
	return htmlPageName(v.id, v.ranges[i].page) + fmt.Sprintf("#byte-%d", v.anchors[a]), nil
}

func (s *htmlReportRenderer) sourceLocation(source htmlSourcePages, loc cover.SourceLocation) string {
	mapping := sourceRangeOffsets(loc, source.source)
	label := s.output.field(htmlLocationRange(loc))
	if mapping.status != "mapped" {
		return label + `<span class="muted"> · Source mapping: ` + s.output.field(mapping.reason) + `</span>`
	}
	return s.sourceOffsetLink(source, mapping.start, label)
}

func (s *htmlReportRenderer) sourceOffsetLink(source htmlSourcePages, offset int, label string) string {
	target, err := source.offset(offset)
	if err != nil {
		s.output.fail(fmt.Errorf("resolve source link: %w", err))
		return ""
	}
	return htmlLink(target, label)
}

func (s *htmlReportRenderer) renderSource(pkg string, file analysis.FileReport, fileID string) htmlSourcePages {
	source, available := s.sources[htmlSourceKey{pkg, file.Path}]
	view := htmlSourcePages{id: htmlSourceAnchor(pkg, file.Path), source: source}
	annotations := sourceAnnotations(file, source)
	var controls strings.Builder
	controls.WriteString(`<fieldset class="source-controls"><legend>Source annotation mode (each page starts in Statement):</legend>`)
	for i, mode := range htmlSourceModes() {
		checked := ""
		if i == 0 {
			checked = " checked"
		}
		fmt.Fprintf(&controls, `<label><input type="radio" name="source-view" value="%s"%s>%s</label>`, mode, checked, mode)
	}
	controls.WriteString(`</fieldset><div class="source-panel" aria-label="Original source"><pre class="source-code"><code>`)
	intro := s.output.paragraph("Package", pkg) + s.output.paragraph("Original source", file.Path) + `<p>` + htmlLink(htmlPageName(fileID+"-annotations", 1), "Annotations and mapping diagnostics") + `</p><p class="legend">covered · partial · uncovered · unknown / unsupported / infeasible / analysis-incomplete</p>`
	if available {
		s.output.write(fileID+".txt", source)
		intro += `<p>` + `<a href="` + fileID + `.txt" download="` + escape(file.Path) + `">Download original source (all bytes)</a>` + `</p>`
	} else {
		intro += `<p class="notice">Original source is not available in this report.</p>`
	}
	p := s.output.pages(htmlSection{id: view.id, title: "Original source", parent: htmlPageName(fileID, 1), intro: intro, before: controls.String(), after: `</code></pre></div>`, rowLimit: 400})
	p.contextHTML = func() string {
		if len(view.ranges) == 0 {
			return ""
		}
		r := view.ranges[len(view.ranges)-1]
		return fmt.Sprintf(`<p>Original lines %d–%d · bytes %d–%d of %d (end exclusive)</p>`, r.firstLine, r.lastLine, r.start, r.end, len(source))
	}
	line := 1
	colorErr := walkHTMLColors(len(source), annotations, func(start, end int, classes string) bool {
		for start < end && s.output.err == nil {
			pieceEnd := min(end, start+1024)
			// One logical line per item, with bounded continuations for giant lines.
			if n := bytes.IndexByte(source[start:pieceEnd], '\n'); n >= 0 {
				pieceEnd = start + n + 1
			}
			text := htmlPrefix(string(source[start:min(end, pieceEnd+4)]), pieceEnd-start)
			if text == "" {
				s.output.fail(fmt.Errorf("split source %q at byte %d: invalid UTF-8 boundary", file.Path, start))
				return false
			}
			pieceEnd = start + len(text)
			endLine := line + strings.Count(strings.TrimSuffix(text, "\n"), "\n")
			item := fmt.Sprintf(`<span class="src-segment %s" id="byte-%d" data-start-offset="%d" data-end-offset="%d" title="Source line %d; see annotations for evidence">%s</span>`, classes, start, start, pieceEnd, line, escape(text))
			page := p.add(item)
			view.anchors = append(view.anchors, start)
			if len(view.ranges) == 0 || view.ranges[len(view.ranges)-1].page != page {
				view.ranges = append(view.ranges, htmlSourceRange{start, pieceEnd, page, line, endLine})
			} else {
				r := &view.ranges[len(view.ranges)-1]
				r.end = pieceEnd
				r.lastLine = endLine
			}
			line += strings.Count(text, "\n")
			start = pieceEnd
		}
		return s.output.err == nil
	})
	if colorErr != nil {
		s.output.fail(fmt.Errorf("color source %q: %w", file.Path, colorErr))
	}
	p.close()
	targets := make(map[string]string)
	for _, fn := range file.Functions {
		target := htmlPageName(htmlFunctionID(pkg, file.Path, fn), 1)
		for _, st := range fn.Statements {
			targets[sourceEntityID("statement", st.Location, sourceRangeOffsets(st.Location, source))] = target
		}
		for _, c := range fn.Clauses {
			targets[c.ClauseID] = target
		}
		for _, n := range fn.NoMatches {
			targets[n.SwitchID] = target
		}
		for _, d := range fn.Decisions {
			did := htmlDecisionAnchor(pkg, d)
			targets[d.DecisionID] = htmlPageName(did, 1)
			for _, c := range d.Conditions {
				cid := htmlPageName(htmlID("condition", did, fmt.Sprint(c.Index)), 1)
				targets[c.ConditionID] = cid
				targets[c.ConditionID+":mcdc-unique"] = cid
				targets[c.ConditionID+":mcdc-masking"] = cid
			}
		}
	}
	list := s.output.pages(htmlSection{id: fileID + "-annotations", title: "Source annotations and mapping diagnostics", parent: htmlPageName(fileID, 1), intro: s.output.paragraph("Package", pkg) + s.output.paragraph("File", file.Path), rowLimit: htmlIndexRows})
	// Annotation details link color ranges to their measurement evidence.
	for _, a := range annotations {
		body := `<article><h2>` + escape(a.Metric) + `</h2>` + s.output.status(a.State) + s.output.paragraph("Entity", a.EntityID) + s.output.paragraph("Evidence", a.Tooltip)
		if a.MappingStatus == "unknown" {
			body += s.output.paragraph("Mapping unknown", a.MappingReason)
		} else {
			body += fmt.Sprintf(`<p>%s · bytes %d–%d</p>`, s.sourceOffsetLink(view, a.StartOffset, "Source range"), a.StartOffset, a.EndOffset)
		}
		if target := targets[a.EntityID]; target != "" {
			body += `<p>` + htmlLink(target, "Inspect evidence") + `</p>`
		}
		list.add(body + `</article>`)
	}
	list.close()
	return view
}
func (s *htmlReportRenderer) sourceContext(source htmlSourcePages, loc cover.SourceLocation) string {
	out := `<p>` + s.sourceLocation(source, loc) + `</p>`
	mapping := sourceRangeOffsets(loc, source.source)
	if mapping.status != "mapped" {
		return out
	}
	start := max(0, mapping.start-160)
	end := min(len(source.source), mapping.end+160)
	end = min(end, start+1024)
	return out + fmt.Sprintf(`<p>Source context · bytes %d–%d</p><pre class="evidence-context">%s</pre>`, start, end, escape(string(source.source[start:end])))
}

func htmlSourceAnchor(pkg, path string) string {
	return fmt.Sprintf("source-%x", sha256.Sum256([]byte(pkg+"\x00"+path)))
}

func htmlLocationRange(location cover.SourceLocation) string {
	if location.Start.Line == 0 {
		return location.File + " (precise location unavailable)"
	}
	return fmt.Sprintf("%s:%d:%d-%d:%d", location.File, location.Start.Line, location.Start.Column, location.End.Line, location.End.Column)
}
