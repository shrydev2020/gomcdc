package measurement

// Settings describes the measured invocation, not the machine rendering it.
// GoTestArgs and GOFLAGS describe the caller's request before path relocation.
type Settings struct {
	InvocationID        string             `json:"invocationId,omitempty"`
	WorkingDirectory    string             `json:"workingDirectory"`
	PackagePatterns     []string           `json:"packagePatterns"`
	Packages            []string           `json:"packages"`
	Coverage            []string           `json:"coverage"`
	GoTestArgs          []string           `json:"goTestArgs"`
	GOFLAGS             string             `json:"goFlags"`
	GoEnvironment       map[string]string  `json:"goEnvironment"`
	IncludeTests        bool               `json:"includeTests"`
	Excludes            []string           `json:"excludes"`
	Timeout             string             `json:"timeout"`
	Strict              bool               `json:"strict"`
	FailUnder           map[string]float64 `json:"failUnder"`
	SourceDigests       map[string]string  `json:"sourceDigests"`
	ModuleConfigDigests map[string]string  `json:"moduleConfigDigests"`
}

// Source uses base64-encoded bytes in JSON to preserve byte offsets even in
// source comments containing invalid UTF-8. Paths are module-relative.
type Source struct {
	PackagePath string `json:"packagePath"`
	Path        string `json:"path"`
	Content     []byte `json:"content"`
	SHA256      string `json:"sha256"`
}
