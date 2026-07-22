package c0_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/c0"
)

func TestBuildInventoryMatchesGoTestCoverProfile(t *testing.T) {
	t.Parallel()

	const source = `package fixture

func Exercise(value int) int {
	total := 0
	if value > 0 {
		total++
	} else if value < 0 {
		total--
	} else {
		total = 2
	}
	for index := 0; index < value; index++ {
		if index == 2 {
			break
		}
		total += index
	}
	switch value {
	case 1:
		total++
	default:
		total += 2
	}
	read := func() int { return total }
	if value == 99 {
		panic("unreachable")
	}
	return read()
}

func Empty() {}
`
	const testSource = `package fixture

import "testing"

func TestExercise(t *testing.T) {
	if got := Exercise(1); got != 2 {
		t.Fatalf("Exercise(1) = %d", got)
	}
}
`
	temporary := t.TempDir()
	writeTestFile(t, filepath.Join(temporary, "go.mod"), []byte("module example.test/fixture\n\ngo 1.22\n"))
	writeTestFile(t, filepath.Join(temporary, "fixture.go"), []byte(source))
	writeTestFile(t, filepath.Join(temporary, "fixture_test.go"), []byte(testSource))
	profilePath := filepath.Join(temporary, "coverage.out")
	command := exec.Command("go", "test", "-count=1", "-covermode=set", "-coverprofile="+profilePath, ".")
	command.Dir = temporary
	command.Env = append(os.Environ(), "GOWORK=off", "GOCACHE="+filepath.Join(temporary, "go-cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go test coverage fixture: %v\n%s", err, output)
	}

	profileContents, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	profile, err := c0.ParseProfile(bytes.NewReader(profileContents))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	inventory, err := c0.BuildInventory("fixture.go", []byte(source))
	if err != nil {
		t.Fatalf("BuildInventory: %v", err)
	}

	type blockShape struct {
		position   c0.SourceRange
		statements int
	}
	got := make([]blockShape, 0, len(inventory.Blocks))
	for _, block := range inventory.Blocks {
		got = append(got, blockShape{position: block.ProfileRange, statements: block.Statements})
	}
	var want []blockShape
	for _, file := range profile.Files {
		if filepath.Base(file.Path) != "fixture.go" {
			continue
		}
		want = make([]blockShape, 0, len(file.Blocks))
		for _, block := range file.Blocks {
			want = append(want, blockShape{position: block.Position, statements: block.Statements})
		}
	}
	if want == nil {
		t.Fatalf("fixture.go absent from profile: %#v", profile.Files)
	}
	sort.Slice(got, func(i, j int) bool { return lessBlockShape(got[i].position, got[j].position) })
	sort.Slice(want, func(i, j int) bool { return lessBlockShape(want[i].position, want[j].position) })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory differs from go test cover profile\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestBuildInventoryPreservesPhysicalAndLineDirectivePositions(t *testing.T) {
	t.Parallel()

	source := []byte("package p\n\n//line virtual.go:100\nfunc F(value bool) int {\n\tif value {\n\t\treturn 1\n\t}\n\treturn 0\n}\n")
	inventory, err := c0.BuildInventory("p/original.go", source)
	if err != nil {
		t.Fatalf("BuildInventory: %v", err)
	}
	if len(inventory.Blocks) == 0 {
		t.Fatal("BuildInventory returned no blocks")
	}
	for _, block := range inventory.Blocks {
		if filepath.Base(block.ProfileFile) != "virtual.go" {
			t.Fatalf("ProfileFile = %q, want virtual.go basename", block.ProfileFile)
		}
		if block.ProfileRange.Start.Line < 100 {
			t.Fatalf("logical range = %#v, want line >= 100", block.ProfileRange)
		}
		if block.PhysicalRange.Start.Line >= 100 {
			t.Fatalf("physical range = %#v, unexpectedly logical", block.PhysicalRange)
		}
	}
}

func lessBlockShape(left, right c0.SourceRange) bool {
	if left.Start.Line != right.Start.Line {
		return left.Start.Line < right.Start.Line
	}
	if left.Start.Column != right.Start.Column {
		return left.Start.Column < right.Start.Column
	}
	if left.End.Line != right.End.Line {
		return left.End.Line < right.End.Line
	}
	return left.End.Column < right.End.Column
}

func writeTestFile(t *testing.T, filename string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}
