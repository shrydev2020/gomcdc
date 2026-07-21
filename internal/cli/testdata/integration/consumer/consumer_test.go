package consumer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRead(t *testing.T) {
	if !Read(true) || Read(false) {
		t.Fatal("unexpected shared result")
	}
}

func TestSingleMeasurementExecution(t *testing.T) {
	markerDir := os.Getenv("GOMCDC_EXECUTION_MARKER_DIR")
	if markerDir == "" {
		return
	}
	marker := filepath.Join(markerDir, "consumer")
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		t.Fatal("consumer package test binary executed more than once in one measurement session")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
