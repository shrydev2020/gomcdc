package allow

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestBooleanDecisions(t *testing.T) {
	for _, test := range []struct {
		a, b bool
		want bool
	}{
		{false, false, false},
		{true, true, true},
		{true, false, false},
	} {
		if got := Allow(test.a, test.b); got != test.want {
			t.Fatalf("Allow(%t, %t) = %t", test.a, test.b, got)
		}
	}
	if !GeneratedGate(true, true) || GeneratedGate(false, true) || GeneratedGate(true, false) {
		t.Fatal("unexpected GeneratedGate result")
	}
	if got := UserNamedCompilerMarker(1); got != 1 {
		t.Fatalf("user marker call after fallthrough = %d, want 1", got)
	}
	if !Any(true, false) || !Any(false, true) || Any(false, false) {
		t.Fatal("unexpected Any result")
	}
	if TaggedMultiline(true, true) != 2 || TaggedMultiline(true, false) != 1 || TaggedMultiline(false, false) != 0 {
		t.Fatal("unexpected build-tagged multiline result")
	}
	if LineMapped(true) != 1 || LineMapped(false) != 0 {
		t.Fatal("unexpected //line-mapped result")
	}
	for _, values := range [][3]bool{
		{false, false, false},
		{true, true, false},
		{true, false, true},
		{true, false, false},
	} {
		Nested(values[0], values[1], values[2])
	}
}

func TestSingleMeasurementExecution(t *testing.T) {
	markerDir := os.Getenv("GOMCDC_EXECUTION_MARKER_DIR")
	if markerDir == "" {
		return
	}
	marker := filepath.Join(markerDir, "allow")
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		t.Fatal("allow package test binary executed more than once in one measurement session")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecursiveAndConcurrentEvaluation(t *testing.T) {
	if !Recursive(3) {
		t.Fatal("Recursive(3) = false")
	}
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			Allow(index%2 == 0, index%3 == 0)
		}(i)
	}
	group.Wait()
}

func TestPanickingConditionIsAborted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MayPanic did not panic")
		}
	}()
	MayPanic(true, func() bool { panic("fixture") })
}

func TestGoexitConditionIsAborted(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		GoexitDecision()
	}()
	<-done
}
