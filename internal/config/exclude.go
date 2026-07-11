package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Excludes implements slash-normalized glob matching with ** support. A **/
// prefix matches zero or more directory levels, so **/mock_*.go also matches a
// file in the module root.
type Excludes struct {
	patterns []*regexp.Regexp
}

func CompileExcludes(globs []string) (Excludes, error) {
	result := Excludes{}
	for _, glob := range globs {
		normalized := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(glob), "./"))
		if normalized == "" {
			return Excludes{}, fmt.Errorf("exclude pattern must not be empty")
		}
		expression, err := globRegexp(normalized)
		if err != nil {
			return Excludes{}, fmt.Errorf("invalid exclude pattern %q: %w", glob, err)
		}
		re, err := regexp.Compile(expression)
		if err != nil {
			return Excludes{}, fmt.Errorf("compile exclude pattern %q: %w", glob, err)
		}
		result.patterns = append(result.patterns, re)
	}
	return result, nil
}

func (e Excludes) Match(relativePath string) bool {
	path := strings.TrimPrefix(filepath.ToSlash(relativePath), "./")
	for _, pattern := range e.patterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func globRegexp(glob string) (string, error) {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(glob); {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i += 2
				if i < len(glob) && glob[i] == '/' {
					b.WriteString("(?:.*/)?")
					i++
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
				i++
			}
		case '?':
			b.WriteString("[^/]")
			i++
		case '[':
			return "", fmt.Errorf("character classes are not supported")
		default:
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
			i++
		}
	}
	b.WriteByte('$')
	return b.String(), nil
}
