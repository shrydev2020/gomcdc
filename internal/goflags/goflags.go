// Package goflags parses and filters the GOFLAGS environment value without
// applying shell expansion. Go accepts quoted flag words, so strings.Fields is
// insufficient for paths containing whitespace.
package goflags

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Split separates a GOFLAGS value using single/double quotes and backslash
// escaping. Quote characters are syntax and are not included in returned
// words.
func Split(value string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	for _, current := range value {
		if escaped {
			word.WriteRune(current)
			inWord = true
			escaped = false
			continue
		}
		if current == '\\' && quote != '\'' {
			escaped = true
			inWord = true
			continue
		}
		if quote != 0 {
			if current == quote {
				quote = 0
			} else {
				word.WriteRune(current)
			}
			inWord = true
			continue
		}
		switch {
		case current == '\'' || current == '"':
			quote = current
			inWord = true
		case unicode.IsSpace(current):
			flush()
		default:
			word.WriteRune(current)
			inWord = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("GOFLAGS ends with an incomplete escape")
	}
	if quote != 0 {
		return nil, fmt.Errorf("GOFLAGS has an unterminated %q quote", quote)
	}
	flush()
	return words, nil
}

// Join returns a canonical GOFLAGS value accepted by Split and cmd/go.
func Join(words []string) string {
	encoded := make([]string, len(words))
	for index, word := range words {
		if word == "" || strings.IndexFunc(word, func(value rune) bool {
			return unicode.IsSpace(value) || value == '\\' || value == '\'' || value == '"'
		}) >= 0 {
			encoded[index] = strconv.Quote(word)
		} else {
			encoded[index] = word
		}
	}
	return strings.Join(encoded, " ")
}

// Name returns a normalized flag name without leading hyphens or '=value'.
func Name(word string) string {
	name := strings.TrimLeft(word, "-")
	if separator := strings.IndexByte(name, '='); separator >= 0 {
		name = name[:separator]
	}
	return name
}

// Contains reports whether a parsed GOFLAGS value contains name.
func Contains(value, name string) (bool, error) {
	words, err := Split(value)
	if err != nil {
		return false, err
	}
	for _, word := range words {
		if Name(word) == name {
			return true, nil
		}
	}
	return false, nil
}

// Without removes owned flags. A true map value means the exact, non-equals
// form consumes one following word as its value.
func Without(value string, owned map[string]bool) (string, error) {
	words, err := Split(value)
	if err != nil {
		return "", err
	}
	filtered := make([]string, 0, len(words))
	for index := 0; index < len(words); index++ {
		word := words[index]
		name := Name(word)
		consumeValue, remove := owned[name]
		if !remove {
			filtered = append(filtered, word)
			continue
		}
		if consumeValue && !strings.Contains(word, "=") && index+1 < len(words) {
			index++
		}
	}
	return Join(filtered), nil
}

// WithoutMeasurementFlags removes flags whose semantics are owned by a
// gocoverage measurement. Unrelated build flags such as tags are preserved.
func WithoutMeasurementFlags(value string) (string, error) {
	return Without(value, map[string]bool{
		"count":        true,
		"cover":        false,
		"covermode":    true,
		"coverpkg":     true,
		"coverprofile": true,
		"json":         false,
	})
}
