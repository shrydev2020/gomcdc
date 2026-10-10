package testcover

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/c0"
)

func TestCollectAcceptsOnlySessionCountersAndMergesProcesses(t *testing.T) {
	inventory, err := c0.BuildInventory("fixture_test.go", []byte("package p\nfunc f() { println(1) }\n"))
	if err != nil {
		t.Fatal(err)
	}
	session := Session{Files: []File{{Source: Source{RelativePath: "fixture_test.go"}, ID: "file", Inventory: inventory}}}
	header := "gomcdc-test-cover-v1 run file\n"
	cases := []struct {
		name  string
		data  []byte
		valid bool
	}{
		{"zero", append([]byte(header), 0), true},
		{"hit", append([]byte(header), 1), true},
		{"wrong-run", append([]byte("gomcdc-test-cover-v1 other file\n"), 1), false},
		{"wrong-file", append([]byte("gomcdc-test-cover-v1 run unknown\n"), 1), false},
		{"truncated", []byte(header), false},
		{"extra", append([]byte(header), 1, 0), false},
		{"invalid-counter", append([]byte(header), 2), false},
		{"diagnostic", []byte("write failed\n"), false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, counterPrefix+"a"), test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := session.Collect(t.Context(), dir, "run")
			if (err == nil) != test.valid {
				t.Fatalf("result=%v error=%v", result, err)
			}
			if !test.valid {
				return
			}
			if len(result) != 1 || len(result[0].Blocks) != 1 || result[0].Blocks[0].Count != uint64(test.data[len(test.data)-1]) {
				t.Fatalf("result=%v", result)
			}
			if err := os.WriteFile(filepath.Join(dir, counterPrefix+"b"), append([]byte(header), 1), 0o600); err != nil {
				t.Fatal(err)
			}
			merged, err := session.Collect(t.Context(), dir, "run")
			if err != nil || merged[0].Blocks[0].Count != 1 {
				t.Fatalf("merged=%v error=%v", merged, err)
			}
		})
	}
	missing, err := session.Collect(t.Context(), t.TempDir(), "run")
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing synthesized evidence: %v %v", missing, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := session.Collect(ctx, t.TempDir(), "run"); err == nil {
		t.Fatal("canceled collection succeeded")
	}
}

func TestRewriteCountersRejectsUnexpectedPlacement(t *testing.T) {
	for _, source := range []string{
		"package p;func f(){ C.Count[0]=2 };var C struct{Count [1]uint32}",
		"package p;func f(){ C.Count[1]=1 };var C struct{Count [1]uint32}",
		"package p;func f(){ C.Count[0]=1;C.Count[0]=1 };var C struct{Count [1]uint32}",
		"package p;func f(){};var C struct{Count [1]uint32}",
	} {
		if _, err := rewriteCounters([]byte(source), "C", "example.test/runtime", "id", 1); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
}

func TestSelectedCoverMetadataHandlesEmptyAndLineDirectedTestSources(t *testing.T) {
	for _, source := range []string{
		"package p\nvar a=1\n",
		"package p\n//line virtual.go:30:4\nfunc helper(){ println(1) }\n",
	} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "file_test.go")
			if err := os.WriteFile(path, []byte(source), 0o444); err != nil {
				t.Fatal(err)
			}
			session, err := Instrument(t.Context(), root, "example.test/p", []Source{{PackagePath: "example.test/p", RelativePath: "file_test.go", CopyPath: path}})
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o444 {
				t.Fatalf("source mode not preserved: %v %v", info, err)
			}
			if len(session.Files) != 1 {
				t.Fatalf("files=%v", session.Files)
			}
			if reflect.DeepEqual(session.Files[0].Inventory, c0.FileInventory{}) {
				t.Fatal("missing inventory")
			}
		})
	}
}
