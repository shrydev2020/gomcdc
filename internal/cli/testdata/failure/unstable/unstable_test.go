package unstable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestControlledFailure(t *testing.T) {
	if !Decision(true) {
		t.Fatal("Decision(true) = false")
	}
	switch os.Getenv("GOMCDC_FAILURE_MODE") {
	case "fail":
		t.Fatal("controlled test failure")
	case "timeout":
		select {}
	case "truncate":
		writeEventFragment(t, `{"type":"terminal"`)
		t.Fatal("controlled test failure after truncated event")
	case "corrupt":
		writeEventFragment(t, "{\"type\":\"terminal\",\"unknown\":1}\n")
		t.Fatal("controlled test failure after corrupt event")
	default:
		if Decision(false) {
			t.Fatal("Decision(false) = true")
		}
	}
}

func writeEventFragment(t *testing.T, contents string) {
	t.Helper()
	directory := os.Getenv("GOMCDC_DATA_DIR")
	if directory == "" {
		t.Fatal("GOMCDC_DATA_DIR is empty")
	}
	path := filepath.Join(directory, "gomcdc-events-v1-controlled.jsonl")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
