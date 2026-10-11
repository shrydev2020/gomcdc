package measurement

import (
	"bytes"
	"encoding/json"
	"fmt"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

// The public report exposes line/column locations. Native evidence additionally
// persists byte ranges, because stable entity identity is defined in bytes.
type byteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type planFields Plan

func (p *Plan) locations(visit func(string, *cover.SourceLocation)) {
	for i := range p.Decisions {
		d := &p.Decisions[i]
		visit(fmt.Sprintf("decision:%x", d.ID), &d.Location)
		visit(fmt.Sprintf("decision-function:%x", d.ID), &d.FunctionLocation)
		for j := range d.Conditions {
			c := &d.Conditions[j]
			visit(fmt.Sprintf("condition:%x", c.ID), &c.Location)
		}
	}
	for i := range p.Clauses {
		c := &p.Clauses[i]
		visit(fmt.Sprintf("clause:%x", c.ID), &c.Location)
		visit(fmt.Sprintf("clause-function:%x", c.ID), &c.FunctionLocation)
	}
	for i := range p.NoMatches {
		n := &p.NoMatches[i]
		visit(fmt.Sprintf("no-match:%x", n.SwitchID), &n.Location)
		visit(fmt.Sprintf("no-match-function:%x", n.SwitchID), &n.FunctionLocation)
	}
}
func (p Plan) MarshalJSON() ([]byte, error) {
	offsets := map[string]byteRange{}
	p.locations(func(key string, l *cover.SourceLocation) { offsets[key] = byteRange{l.StartOffset, l.EndOffset} })
	return json.Marshal(struct {
		planFields
		ByteRanges map[string]byteRange `json:"byteRanges"`
	}{planFields(p), offsets})
}
func (p *Plan) UnmarshalJSON(data []byte) error {
	var wire struct {
		planFields
		ByteRanges map[string]byteRange `json:"byteRanges"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("decode measurement plan: %w", err)
	}
	*p = Plan(wire.planFields)
	var missing bool
	p.locations(func(key string, l *cover.SourceLocation) {
		span, ok := wire.ByteRanges[key]
		if !ok {
			missing = true
		}
		l.StartOffset, l.EndOffset = span.Start, span.End
		delete(wire.ByteRanges, key)
	})
	if missing || len(wire.ByteRanges) > 0 {
		return fmt.Errorf("plan byte ranges do not match source entities")
	}
	return nil
}
