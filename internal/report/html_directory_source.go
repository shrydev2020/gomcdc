package report

import (
	"bytes"
	"cmp"
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
func (v htmlSourcePages) location(loc cover.SourceLocation) string {
	mapping := sourceRangeOffsets(loc, v.source)
	if mapping.status != "mapped" {
		return v.first()
	}
	return v.offset(mapping.start)
}
func (v htmlSourcePages) offset(offset int) string {
	// Page ranges partition the original source; binary search avoids a scan per
	// finding when a single source file has thousands of pages.
	i, _ := slices.BinarySearchFunc(v.ranges, offset, func(r htmlSourceRange, n int) int {
		if n < r.start {
			return 1
		}
		if n >= r.end {
			return -1
		}
		return 0
	})
	if i >= len(v.ranges) {
		return v.first()
	}
	a, found := slices.BinarySearch(v.anchors, offset)
	if !found {
		a--
	}
	if a < 0 {
		a = 0
	}
	return htmlPageName(v.id, v.ranges[i].page) + fmt.Sprintf("#byte-%d", v.anchors[a])
}

type htmlColorEvent struct{ offset, mode, severity, delta int }

func htmlAnnotationMode(metric string) int {
	switch metric {
	case "statement":
		return 0
	case "decision":
		return 1
	case "condition":
		return 2
	case "mcdc-unique", "mcdc-masking":
		return 3
	case "clause-body", "clause-selection":
		return 4
	default:
		return -1
	}
}

// walkHTMLColors retains fixed-size counters for color selection. It never
// copies the active annotation set at each interval (quadratic for nesting).
func walkHTMLColors(length int, annotations []SourceAnnotation, yield func(start, end int, classes string) bool) {
	events := make([]htmlColorEvent, 0, len(annotations)*2+1)
	for _, a := range annotations {
		mode, severity := htmlAnnotationMode(a.Metric), htmlStateSeverity(a.State)
		if mode < 0 || severity == 0 || a.StartOffset < 0 || a.EndOffset <= a.StartOffset || a.EndOffset > length {
			continue
		}
		events = append(events, htmlColorEvent{a.StartOffset, mode, severity, 1}, htmlColorEvent{a.EndOffset, mode, severity, -1})
	}
	events = append(events, htmlColorEvent{offset: length})
	slices.SortFunc(events, func(a, b htmlColorEvent) int { return cmp.Compare(a.offset, b.offset) })
	var active [6][5]int
	names := htmlSourceModes()
	states := []string{"none", "covered", "partial", "uncovered", "attention"}
	start := 0
	for i := 0; i < len(events); {
		end := events[i].offset
		if end > start {
			var classes []string
			for mode := range active {
				for severity := 4; severity > 0; severity-- {
					if active[mode][severity] > 0 {
						classes = append(classes, "view-"+names[mode]+"-"+states[severity])
						break
					}
				}
			}
			if !yield(start, end, strings.Join(classes, " ")) {
				return
			}
			start = end
		}
		for i < len(events) && events[i].offset == end {
			e := events[i]
			if e.delta != 0 {
				active[e.mode][e.severity] += e.delta
				active[5][e.severity] += e.delta
			}
			i++
		}
	}
}

func (s *htmlDirectory) renderSource(pkg string, file analysis.FileReport, fileID string) htmlSourcePages {
	source, available := s.sources[pkg+"\x00"+file.Path]
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
	intro := s.paragraph("Original source", file.Path) + `<p>` + htmlLink(htmlPageName(fileID+"-annotations", 1), "Annotations and mapping diagnostics") + `</p><p class="legend">covered · partial · uncovered · unknown / unsupported / infeasible / analysis-incomplete</p>`
	if available {
		s.write(fileID+".txt", source)
		intro += `<p>` + `<a href="` + fileID + `.txt" download="` + escape(file.Path) + `">Download original source (all bytes)</a>` + `</p>`
	} else {
		intro += `<p class="notice">Original source is not available in this report.</p>`
	}
	p := s.pages(view.id, "Original source", htmlPageName(fileID, 1), intro, controls.String(), `</code></pre></div>`, 400)
	p.extra = func() string {
		if len(view.ranges) == 0 {
			return ""
		}
		r := view.ranges[len(view.ranges)-1]
		return fmt.Sprintf(`<p>Original lines %d–%d · bytes %d–%d of %d (end exclusive)</p>`, r.firstLine, r.lastLine, r.start, r.end, len(source))
	}
	line := 1
	walkHTMLColors(len(source), annotations, func(start, end int, classes string) bool {
		for start < end && s.err == nil {
			pieceEnd := min(end, start+1024)
			// One logical line per item, with bounded continuations for giant lines.
			if n := bytes.IndexByte(source[start:pieceEnd], '\n'); n >= 0 {
				pieceEnd = start + n + 1
			}
			text := htmlPrefix(string(source[start:min(end, pieceEnd+4)]), pieceEnd-start)
			if text == "" {
				text = string(source[start:pieceEnd])
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
		return s.err == nil
	})
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
	list := s.pages(fileID+"-annotations", "Source annotations and mapping diagnostics", htmlPageName(fileID, 1), s.paragraph("File", file.Path), "", "", htmlIndexRows)
	// Keep each detail once; color spans only contain fixed-size color state.
	for _, a := range annotations {
		body := `<article><h2>` + escape(a.Metric) + `</h2>` + s.status(a.State) + s.paragraph("Entity", a.EntityID) + s.paragraph("Evidence", a.Tooltip)
		if a.MappingStatus == "unknown" {
			body += s.paragraph("Mapping unknown", a.MappingReason)
		} else {
			body += fmt.Sprintf(`<p>%s · bytes %d–%d</p>`, htmlLink(view.offset(a.StartOffset), "Source range"), a.StartOffset, a.EndOffset)
		}
		if target := targets[a.EntityID]; target != "" {
			body += `<p>` + htmlLink(target, "Inspect evidence") + `</p>`
		}
		list.add(body + `</article>`)
	}
	list.close()
	return view
}
func (s *htmlDirectory) sourceContext(source htmlSourcePages, loc cover.SourceLocation) string {
	out := `<p>` + s.link(source.location(loc), htmlLocationRange(loc)) + `</p>`
	mapping := sourceRangeOffsets(loc, source.source)
	if mapping.status != "mapped" {
		return out + s.paragraph("Source mapping", mapping.reason)
	}
	start := max(0, mapping.start-160)
	end := min(len(source.source), mapping.end+160)
	end = min(end, start+1024)
	return out + fmt.Sprintf(`<p>Source context · bytes %d–%d</p><pre class="evidence-context">%s</pre>`, start, end, escape(string(source.source[start:end])))
}
