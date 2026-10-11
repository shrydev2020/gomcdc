package measurement

import (
	"bytes"
	"context"
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

func TestDecodePreservesReadCausesAtEnvelopeAndEnd(t *testing.T) {
	data, err := Encode(emptyArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("fixture read failure")
	for _, test := range []struct {
		name   string
		reader io.Reader
	}{
		{"envelope", failedReader{cause}},
		{"end", io.MultiReader(bytes.NewReader(data), failedReader{cause})},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode(test.reader)
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), "measurement") {
				t.Fatalf("read cause or artifact context lost: %v", err)
			}
		})
	}
	_, err = Decode(strings.NewReader(`{"format":!}`))
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Fatalf("JSON error type lost: %v", err)
	}
}

func TestPublishPreservesCauseAndDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "result.json")
	err := Write(t.Context(), path, emptyArchive(t))
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Fatalf("destination or cause lost: %v", err)
	}
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Fatalf("filesystem error type lost: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = Write(ctx, path, emptyArchive(t))
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), path) {
		t.Fatalf("cancellation identity lost: %v", err)
	}
	invalid := emptyArchive(t)
	invalid.Runs[0].RuntimeID = "invalid"
	err = Write(t.Context(), path, invalid)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), invalid.Runs[0].ID) {
		t.Fatalf("encoding failure lost destination/run: %v", err)
	}
}

func TestMergeFailureIdentifiesInputAndExecution(t *testing.T) {
	good, bad := emptyArchive(t), emptyArchive(t)
	bad.Runs[0].ID = strings.Repeat("b", 32)
	bad.Runs[0].Settings.InvocationID = bad.Runs[0].ID
	bad.Runs[0].RuntimeID = "invalid"
	_, err := Merge(t.Context(), []Archive{good, bad}, 0)
	if err == nil || !strings.Contains(err.Error(), "input 2") || !strings.Contains(err.Error(), bad.Runs[0].ID) {
		t.Fatalf("ambiguous merge error: %v", err)
	}
}
