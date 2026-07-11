package loader

import (
	"reflect"
	"testing"
)

func TestBuildFlags(t *testing.T) {
	t.Parallel()
	got, err := BuildFlags([]string{"-run", "TestOne", "-tags", "integration,unix", "-race=true", "-mod=mod", "-args", "-tags=ignored"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-tags", "integration,unix", "-race=true", "-mod=mod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildFlags() = %#v, want %#v", got, want)
	}
}

func TestBuildFlagsMissingValue(t *testing.T) {
	t.Parallel()
	if _, err := BuildFlags([]string{"-tags"}); err == nil {
		t.Fatal("BuildFlags() error = nil, want missing-value error")
	}
}
