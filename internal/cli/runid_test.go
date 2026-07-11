package cli

import (
	"bytes"
	"errors"
	"testing"
)

func TestReadRunID(t *testing.T) {
	t.Parallel()
	got, err := readRunID(bytes.NewReader([]byte("0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	if want := "30313233343536373839616263646566"; got != want {
		t.Fatalf("readRunID() = %q, want %q", got, want)
	}
	if _, err := readRunID(failingReader{}); err == nil {
		t.Fatal("readRunID(failure) error = nil")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
