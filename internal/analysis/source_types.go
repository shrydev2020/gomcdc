package analysis

// SourceFileInput is immutable original source supplied by the analyzer.
type SourceFileInput struct {
	PackagePath string
	Path        string
	Source      []byte
}
