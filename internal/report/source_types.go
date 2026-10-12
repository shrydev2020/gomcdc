package report

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
