package resultfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failedReader struct{ cause error }

func (r failedReader) Read([]byte) (int, error) { return 0, r.cause }

func TestDecodeRetainsUnderlyingReadAndJSONErrors(t *testing.T) {
	data, err := Encode(fixture())
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("fixture read failure")
	for _, reader := range []io.Reader{failedReader{cause}, io.MultiReader(bytes.NewReader(data), failedReader{cause})} {
		_, err := Decode(reader)
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "saved result") {
			t.Fatalf("reader cause/context lost: %v", err)
		}
	}
	_, err = Decode(strings.NewReader(`{"format":!}`))
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Fatalf("JSON error type lost: %v", err)
	}
}

func TestWriteErrorNamesDestinationAndPreservesFilesystemCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "result.json")
	err := Write(path, fixture())
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Fatalf("destination or cause lost: %v", err)
	}
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Fatalf("filesystem error type lost: %v", err)
	}
	invalid := fixture()
	invalid.Measurement.Runs[0].RuntimeID = "invalid"
	err = Write(path, invalid)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), invalid.Measurement.Runs[0].ID) {
		t.Fatalf("encoding failure lost destination/run: %v", err)
	}
}
