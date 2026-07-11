package allow

import (
	"errors"
	"os"
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
	if !Any(true, false) || !Any(false, true) || Any(false, false) {
		t.Fatal("unexpected Any result")
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

func TestMeasurementWorkspacesAreIsolated(t *testing.T) {
	if os.Getenv("GOMCDC_ISOLATION_FIXTURE") != "1" {
		return
	}
	const marker = ".standard-run-marker"
	if os.Getenv("GOMCDC_DATA_DIR") == "" {
		if err := os.WriteFile(marker, []byte("standard-cover"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AST measurement observed standard-cover workspace state: %v", err)
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
