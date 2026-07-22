package loader

import (
	"reflect"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/gotestargs"
)

func TestBuildFlags(t *testing.T) {
	t.Parallel()
	arguments, err := gotestargs.Parse([]string{"-run", "TestOne", "-tags", "integration,unix", "-race=true", "-mod=mod", "-args", "-tags=ignored"})
	if err != nil {
		t.Fatal(err)
	}
	got := BuildFlags(arguments)
	want := []string{"-tags=integration,unix", "-race=true", "-mod=mod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildFlags() = %#v, want %#v", got, want)
	}
}

func TestBuildFlagsDoNotReinterpretAnotherFlagsValue(t *testing.T) {
	t.Parallel()
	arguments, err := gotestargs.Parse([]string{"-run", "-tags", "-mod=mod"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := BuildFlags(arguments), []string{"-mod=mod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildFlags() = %#v, want %#v", got, want)
	}
}
