package config

import "testing"

func TestExcludes(t *testing.T) {
	t.Parallel()
	matcher, err := CompileExcludes([]string{"**/*.generated.go", "**/mock_*.go", "internal/private/**"})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{
		"root.generated.go":          true,
		"pkg/nested.generated.go":    true,
		"mock_user.go":               true,
		"pkg/mock_user.go":           true,
		"internal/private/value.go":  true,
		"internal/public/value.go":   false,
		"pkg/not-generated.go":       false,
		"pkg/mock_user.go.unrelated": false,
	}
	for name, want := range tests {
		if got := matcher.Match(name); got != want {
			t.Errorf("Match(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestExcludesRejectsUnsupportedClass(t *testing.T) {
	t.Parallel()
	if _, err := CompileExcludes([]string{"[ab].go"}); err == nil {
		t.Fatal("CompileExcludes() error = nil, want error")
	}
}
