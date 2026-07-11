package report

import (
	"bytes"
	"encoding/json"
	"io"
)

// RenderJSON builds input and returns indented schema-v1 JSON ending in a
// newline.
func RenderJSON(input Input) ([]byte, error) {
	var buffer bytes.Buffer
	if err := WriteJSON(&buffer, input); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// WriteJSON builds input and writes deterministic, indented schema-v1 JSON.
func WriteJSON(writer io.Writer, input Input) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(Build(input))
}
