package goflags_test

import (
	"reflect"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/goflags"
)

func TestSplitJoinAndFilter(t *testing.T) {
	t.Parallel()
	words, err := goflags.Split(`-tags=integration "-overlay=/tmp/a b.json" -cover -covermode atomic -x`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-tags=integration", "-overlay=/tmp/a b.json", "-cover", "-covermode", "atomic", "-x"}
	if !reflect.DeepEqual(words, want) {
		t.Fatalf("words = %#v, want %#v", words, want)
	}
	joined := goflags.Join(words)
	roundTrip, err := goflags.Split(joined)
	if err != nil || !reflect.DeepEqual(roundTrip, want) {
		t.Fatalf("round trip = %#v, %v", roundTrip, err)
	}
	filtered, err := goflags.Without(joined, map[string]bool{"cover": false, "covermode": true, "overlay": true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := goflags.Split(filtered); !reflect.DeepEqual(got, []string{"-tags=integration", "-x"}) {
		t.Fatalf("filtered = %q (%#v)", filtered, got)
	}
}

func TestSplitRejectsMalformedQuoting(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`"unterminated`, `-tags=x\`} {
		if _, err := goflags.Split(value); err == nil {
			t.Errorf("Split(%q) error = nil", value)
		}
	}
}
