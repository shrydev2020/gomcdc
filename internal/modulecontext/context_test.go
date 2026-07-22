package modulecontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverSnapshotsSingleModuleWorkspace(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "module")
	dependency := filepath.Join(root, "dependency")
	writeContextFile(t, filepath.Join(module, "go.mod"), "module example.test/module\n\ngo 1.26\n")
	writeContextFile(t, filepath.Join(dependency, "go.mod"), "module example.test/dependency\n\ngo 1.26\n")
	goWork := filepath.Join(root, "go.work")
	writeContextFile(t, goWork, "go 1.26\n\nuse ./module\n\nreplace example.test/dependency => ./dependency\n")
	t.Setenv("GOWORK", goWork)

	settings, err := Discover(context.Background(), module)
	if err != nil {
		t.Fatal(err)
	}
	canonicalGoWork, err := filepath.EvalSymlinks(goWork)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Active() || settings.GoWorkPath() != canonicalGoWork {
		t.Fatalf("settings = %#v", settings)
	}
	if err := settings.AssertMainModule(module); err != nil {
		t.Fatal(err)
	}
	// Mutation after discovery must not alter the configuration used by the
	// copied test workspace.
	writeContextFile(t, goWork, "go 1.26\n\nuse ./dependency\n")
	if err := settings.AssertSourceUnchanged(); err == nil || !strings.Contains(err.Error(), "changed during package loading") {
		t.Fatalf("AssertSourceUnchanged() error = %v", err)
	}
	copiedWorkspace := t.TempDir()
	copiedModule := filepath.Join(copiedWorkspace, "module")
	contents, err := settings.RelocatedGoWork(context.Background(), module, copiedWorkspace, copiedModule)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	canonicalDependency, err := filepath.EvalSymlinks(dependency)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "use ./module") || !strings.Contains(text, "=> "+canonicalDependency) {
		t.Fatalf("relocated snapshot lost module settings:\n%s", text)
	}
}

func TestDiscoverRejectsMultipleMainModules(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		writeContextFile(t, filepath.Join(root, name, "go.mod"), "module example.test/"+name+"\n\ngo 1.26\n")
	}
	goWork := filepath.Join(root, "go.work")
	writeContextFile(t, goWork, "go 1.26\n\nuse (\n\t./first\n\t./second\n)\n")
	t.Setenv("GOWORK", goWork)
	if _, err := Discover(context.Background(), filepath.Join(root, "first")); err == nil || !strings.Contains(err.Error(), "has 2 main modules") {
		t.Fatalf("Discover() error = %v", err)
	}
}

func writeContextFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
