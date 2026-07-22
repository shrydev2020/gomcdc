package gotestargs

import (
	"reflect"
	"testing"
)

func TestKnownValuedFlagOwnsHyphenPrefixedValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arguments []string
		flag      string
		value     string
	}{
		{arguments: []string{"-run", "-count"}, flag: "run", value: "-count"},
		{arguments: []string{"-run", "-tags"}, flag: "run", value: "-tags"},
		{arguments: []string{"-run", "-args"}, flag: "run", value: "-args"},
		{arguments: []string{"-exec", "-overlay"}, flag: "exec", value: "-overlay"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.flag+"="+test.value, func(t *testing.T) {
			parsed, err := Parse(test.arguments)
			if err != nil {
				t.Fatal(err)
			}
			flags := parsed.Flags()
			if len(flags) != 1 || flags[0].Name() != test.flag {
				t.Fatalf("flags = %#v", flags)
			}
			if value, ok := flags[0].Value(); !ok || value != test.value {
				t.Fatalf("value = %q, %t; want %q, true", value, ok, test.value)
			}
			if _, boundary := parsed.BinaryArgs(); boundary {
				t.Fatal("valued flag token became a binary-argument boundary")
			}
		})
	}
}

func TestActualArgsMarkerStartsBinaryArguments(t *testing.T) {
	t.Parallel()
	parsed, err := Parse([]string{"-run=TestX", "-args", "-count=2", "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Prefix(), []string{"-run=TestX"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix = %#v, want %#v", got, want)
	}
	if got, boundary := parsed.BinaryArgs(); !boundary || !reflect.DeepEqual(got, []string{"-count=2", "fixture"}) {
		t.Fatalf("binary = %#v, boundary=%t", got, boundary)
	}
}

func TestFlagTerminatorRemainsAResultingBinaryArgument(t *testing.T) {
	t.Parallel()
	parsed, err := Parse([]string{"-run=TestX", "--", "-custom"})
	if err != nil {
		t.Fatal(err)
	}
	if got, boundary := parsed.BinaryArgs(); !boundary || !reflect.DeepEqual(got, []string{"--", "-custom"}) {
		t.Fatalf("binary = %#v, boundary=%t", got, boundary)
	}
}

func TestUnknownFlagMatchesGoTestScanningBoundary(t *testing.T) {
	t.Parallel()
	parsed, err := Parse([]string{"-custom", "value", "-tags", "integration", "literal", "-run=ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Prefix(), []string{"-custom", "value", "-tags", "integration"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix = %#v, want %#v", got, want)
	}
	if got, boundary := parsed.BinaryArgs(); !boundary || !reflect.DeepEqual(got, []string{"literal", "-run=ignored"}) {
		t.Fatalf("binary = %#v, boundary=%t", got, boundary)
	}
}

func TestWithoutAndWithValueUseSemanticFlags(t *testing.T) {
	t.Parallel()
	parsed, err := Parse([]string{
		"-run", "-modfile", "-modfile", "/source/analysis.mod", "-test.count=3", "-args", "-modfile=binary.mod",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed = parsed.Without("count").WithValue("modfile", "/workspace/gomcdc.mod")
	if got, want := parsed.Prefix(), []string{"-run", "-modfile", "-modfile=/workspace/gomcdc.mod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix = %#v, want %#v", got, want)
	}
	if got, boundary := parsed.BinaryArgs(); !boundary || !reflect.DeepEqual(got, []string{"-modfile=binary.mod"}) {
		t.Fatalf("binary = %#v, boundary=%t", got, boundary)
	}
}

func TestMissingKnownFlagValueIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]string{"-tags"}); err == nil {
		t.Fatal("Parse(-tags) error = nil")
	}
}
