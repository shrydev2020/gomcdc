package report

// SourceFileView is the source-centered representation used by HTML reports.
type SourceFileView struct {
	Path        string             `json:"path"`
	Source      string             `json:"source"`
	Annotations []SourceAnnotation `json:"annotations"`
	Diagnostics []SourceDiagnostic `json:"diagnostics,omitempty"`
}

// SourceAnnotation identifies one metric over an original source byte range.
type SourceAnnotation struct {
	StartOffset   int    `json:"startOffset"`
	EndOffset     int    `json:"endOffset"`
	Metric        string `json:"metric"`
	EntityID      string `json:"entityId"`
	State         string `json:"state"`
	Tooltip       string `json:"tooltip"`
	MappingStatus string `json:"mappingStatus,omitempty"`
	MappingReason string `json:"mappingReason,omitempty"`
}

// SourceDiagnostic explains why a coverage entity could not be projected onto
// the original source. Mapping failure is separate from coverage state.
type SourceDiagnostic struct {
	Metric   string `json:"metric"`
	EntityID string `json:"entityId"`
	Reason   string `json:"reason"`
}
