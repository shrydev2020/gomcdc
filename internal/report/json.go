package report

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

// RenderJSONReport renders an already-built report without rebuilding it.
func RenderJSONReport(report analysis.Result) ([]byte, error) {
	var buffer bytes.Buffer
	if err := WriteJSONReport(&buffer, report); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// WriteJSONReport writes an already-built report without rebuilding it.
func WriteJSONReport(writer io.Writer, report analysis.Result) error {
	if report.Errors == nil {
		report.Errors = []analysis.ReportError{}
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
