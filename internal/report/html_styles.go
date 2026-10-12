package report

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
)

//go:embed template/report.css
var htmlStyles string

//go:embed template/pages.css
var htmlPageStyles string

func renderHTMLStyles() ([]byte, error) {
	t, err := template.New("report.css").Funcs(template.FuncMap{"sourceModes": htmlSourceModes}).Parse(htmlStyles)
	if err != nil {
		return nil, fmt.Errorf("parse report stylesheet: %w", err)
	}
	var css bytes.Buffer
	if err := t.Execute(&css, nil); err != nil {
		return nil, fmt.Errorf("render report stylesheet: %w", err)
	}
	css.WriteString(htmlPageStyles)
	return css.Bytes(), nil
}
