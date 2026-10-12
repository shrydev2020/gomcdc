package report

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Page budgets include navigation and escaped text.
const (
	htmlPageBytes             = 1 << 20
	htmlPageElements          = 5000
	htmlBodyBytes             = 512 << 10
	htmlBodyElements          = 3500
	htmlFieldBytes            = 1024
	htmlTextChunkBytes        = 16 << 10
	htmlIndexRows             = 100
	htmlEvaluationRowsPerPage = 200
	htmlEvaluationColumns     = 16
)

// htmlPageWriter writes assets and retains the first output failure.
type htmlPageWriter struct {
	ctx                 context.Context
	dir, module, footer string
	err                 error
}

type htmlSection struct {
	id, title, parent, intro, before, after string
	rowLimit                                int
}

// htmlPageSeries buffers one page of a paginated section.
type htmlPageSeries struct {
	htmlSection
	writer                      *htmlPageWriter
	contextHTML                 func() string
	body                        strings.Builder
	page, rows, total, elements int
}

func htmlID(kind string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		io.WriteString(h, p)
	}
	return fmt.Sprintf("%s-%x", kind, h.Sum(nil))
}
func htmlPageName(id string, page int) string { return fmt.Sprintf("%s-%d.html", id, page) }
func escape(s string) string                  { return html.EscapeString(s) }
func htmlLink(target, label string) string {
	return `<a href="` + escape(target) + `">` + label + `</a>`
}
func htmlElements(s string) int {
	n := 0
	for part := range strings.SplitSeq(s, "<") {
		if len(part) > 0 && ((part[0] >= 'a' && part[0] <= 'z') || (part[0] >= 'A' && part[0] <= 'Z')) {
			n++
		}
	}
	return n
}
func htmlPrefix(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func (s *htmlPageWriter) fail(err error) {
	if s.err == nil {
		s.err = err
	}
}

func (s *htmlPageWriter) write(name string, content []byte) {
	if s.err != nil {
		return
	}
	if err := s.ctx.Err(); err != nil {
		s.err = fmt.Errorf("write HTML asset %q: %w", name, err)
		return
	}
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		s.err = fmt.Errorf("create HTML asset %q: %w", name, err)
		return
	}
	_, err = f.Write(content)
	if err != nil {
		s.err = fmt.Errorf("write HTML asset %q: %w", name, err)
	}
	if err := f.Close(); err != nil {
		s.err = errors.Join(s.err, fmt.Errorf("close HTML asset %q: %w", name, err))
	}
}
func (s *htmlPageWriter) pages(section htmlSection) *htmlPageSeries {
	section.title = htmlPrefix(section.title, 256)
	return &htmlPageSeries{writer: s, htmlSection: section, page: 1}
}
func (p *htmlPageSeries) link() string { return htmlPageName(p.id, 1) }
func (p *htmlPageSeries) add(item string) int {
	if p.writer.err != nil {
		return p.page
	}
	nodes := htmlElements(item)
	if len(item) > htmlBodyBytes || nodes > htmlBodyElements {
		p.writer.err = fmt.Errorf("HTML item exceeds page budget in %s", p.id)
		return p.page
	}
	if p.rows > 0 && (p.rows >= p.rowLimit || p.body.Len()+len(item) > htmlBodyBytes || p.elements+nodes > htmlBodyElements) {
		p.flush(true)
		if p.writer.err != nil {
			return p.page
		}
	}
	p.body.WriteString(item)
	p.rows++
	p.total++
	p.elements += nodes
	return p.page
}
func (p *htmlPageSeries) close() { p.flush(false) }
func (p *htmlPageSeries) nav(next bool) string {
	if p.page == 1 && !next {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<nav class="pagination" aria-label="Pages">`)
	if p.page > 1 {
		b.WriteString(htmlLink(p.link(), "First"))
		b.WriteString(htmlLink(htmlPageName(p.id, p.page-1), "Previous"))
	}
	fmt.Fprintf(&b, `<span>Page %d · items %d–%d</span>`, p.page, max(0, p.total-p.rows+1), p.total)
	if next {
		b.WriteString(htmlLink(htmlPageName(p.id, p.page+1), "Next"))
	}
	b.WriteString(`</nav>`)
	return b.String()
}
func (p *htmlPageSeries) flush(next bool) {
	s := p.writer
	if s.err != nil {
		return
	}
	content, err := p.document(next)
	if err != nil {
		s.fail(err)
		return
	}
	if len(content) > htmlPageBytes || htmlElements(content) > htmlPageElements {
		s.err = fmt.Errorf("HTML page budget exceeded: %s", p.id)
		return
	}
	s.write(htmlPageName(p.id, p.page), []byte(content))
	p.body.Reset()
	p.rows = 0
	p.elements = 0
	p.page++
}

// field writes long values to text pages keyed by their complete contents.
func (s *htmlPageWriter) field(value string) string {
	if s.err != nil {
		return ""
	}
	if len(value) <= htmlFieldBytes {
		if strings.ContainsAny(value, "\t\r\n") || strings.Contains(value, "  ") {
			return `<span class="field">` + escape(value) + `</span>`
		}
		return escape(value)
	}
	id := htmlID("text", value)
	if _, err := os.Stat(filepath.Join(s.dir, htmlPageName(id, 1))); errors.Is(err, os.ErrNotExist) {
		p := s.pages(htmlSection{id: id, title: "Full text", parent: "overview-1.html", before: `<pre class="evidence-context">`, after: `</pre>`, rowLimit: 8})
		for rest := value; rest != "" && s.err == nil; {
			chunk := htmlPrefix(rest, htmlTextChunkBytes)
			if chunk == "" {
				s.fail(fmt.Errorf("split text field %q: invalid UTF-8 boundary", id))
				return ""
			}
			p.add(escape(chunk))
			rest = rest[len(chunk):]
		}
		p.close()
	} else if err != nil {
		s.err = fmt.Errorf("inspect text page %q: %w", id, err)
	}
	return `<span class="field">` + escape(htmlPrefix(value, 256)) + `… ` + htmlLink(htmlPageName(id, 1), "Full text") + `</span>`
}
func (s *htmlPageWriter) paragraph(label, value string) string {
	return "<p><strong>" + escape(label) + ":</strong> " + s.field(value) + "</p>"
}
func (s *htmlPageWriter) status(value string) string {
	return `<span class="status status-` + escape(htmlPrefix(value, 64)) + `">` + s.field(value) + `</span>`
}
func (p *htmlPageSeries) pageContext() string {
	if p.contextHTML != nil {
		return p.contextHTML()
	}
	return ""
}

func (s *htmlPageWriter) link(target, value string) string {
	label := htmlPrefix(value, 256)
	if len(value) <= htmlFieldBytes {
		return htmlLink(target, escape(value))
	}
	return htmlLink(target, escape(label)+"…") + " " + s.field(value)
}

//go:embed template/page.html
var htmlPageDocument string

var htmlPageTemplate = template.Must(template.New("page").Parse(htmlPageDocument))

type htmlPageDocumentView struct {
	Title, Module, Parent                    string
	Intro, Context, Navigation, Body, Footer template.HTML
	Empty                                    bool
}

func (p *htmlPageSeries) document(next bool) (string, error) {
	view := htmlPageDocumentView{
		Title: p.title, Module: htmlPrefix(p.writer.module, 256), Parent: p.parent,
		Intro: template.HTML(p.intro), Context: template.HTML(p.pageContext()),
		Navigation: template.HTML(p.nav(next)),
		Body:       template.HTML(p.before + p.body.String() + p.after),
		Footer:     template.HTML(p.writer.footer), Empty: p.rows == 0,
	}
	var document strings.Builder
	if err := htmlPageTemplate.Execute(&document, view); err != nil {
		return "", fmt.Errorf("render HTML page %q: %w", htmlPageName(p.id, p.page), err)
	}
	return document.String(), nil
}
