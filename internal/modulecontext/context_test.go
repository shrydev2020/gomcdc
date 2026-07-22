package modulecontext

import (
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

	settings, err := Discover(t.Context(), DiscoverOptions{Dir: module})
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
	contents, err := settings.RelocatedGoWork(t.Context(), module, copiedWorkspace, copiedModule)
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
	if _, err := Discover(t.Context(), DiscoverOptions{Dir: filepath.Join(root, "first")}); err == nil || !strings.Contains(err.Error(), "has 2 main modules") {
		t.Fatalf("Discover() error = %v", err)
	}
}

func TestDiscoverFreezesAndRelocatesAlternateModFileAndSum(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "module")
	writeContextFile(t, filepath.Join(module, "go.mod"), "module example.test/module\n\ngo 1.26\n")
	writeContextFile(t, filepath.Join(module, "dependency", "go.mod"), "module example.test/dependency\n\ngo 1.26\n")
	alternate := filepath.Join(root, "config", "analysis.mod")
	alternateSum := filepath.Join(root, "config", "analysis.sum")
	writeContextFile(t, alternate, "module example.test/module\n\ngo 1.26\n\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => ./dependency\n")
	writeContextFile(t, alternateSum, "frozen sum bytes\n")
	t.Setenv("GOWORK", "off")

	settings, err := Discover(t.Context(), DiscoverOptions{
		Dir: module, BuildFlags: []string{"-modfile=" + alternate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.HasAlternateModFile() || settings.AlternateModFilePath() != alternate {
		t.Fatalf("alternate modfile = %q", settings.AlternateModFilePath())
	}
	copiedModule := filepath.Join(t.TempDir(), "module")
	mod, sum, sumSet, err := settings.RelocatedAlternateMod(t.Context(), copiedModule)
	if err != nil {
		t.Fatal(err)
	}
	if !sumSet || string(sum) != "frozen sum bytes\n" {
		t.Fatalf("relocated sum = set %t bytes %q", sumSet, sum)
	}
	wantReplacement := filepath.Join(copiedModule, "dependency")
	if !strings.Contains(string(mod), "=> "+wantReplacement) {
		t.Fatalf("relocated alternate modfile does not target copied module:\n%s", mod)
	}
	writeContextFile(t, alternateSum, "changed\n")
	if err := settings.AssertSourceUnchanged(); err == nil || !strings.Contains(err.Error(), "changed during package loading") {
		t.Fatalf("AssertSourceUnchanged() error = %v", err)
	}
}

func TestAlternateModFileExplicitFlagOverridesGOFLAGS(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "module")
	writeContextFile(t, filepath.Join(module, "go.mod"), "module example.test/module\n\ngo 1.26\n")
	fromEnvironment := filepath.Join(root, "environment.mod")
	fromCommand := filepath.Join(root, "command.mod")
	writeContextFile(t, fromEnvironment, "module example.test/module\n\ngo 1.26\n")
	writeContextFile(t, fromCommand, "module example.test/module\n\ngo 1.26\n")
	t.Setenv("GOWORK", "off")

	settings, err := Discover(t.Context(), DiscoverOptions{
		Dir: module, GOFLAGS: "-modfile=" + fromEnvironment,
		BuildFlags: []string{"-modfile=" + fromCommand},
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.AlternateModFilePath() != fromCommand {
		t.Fatalf("selected alternate modfile = %q, want explicit %q", settings.AlternateModFilePath(), fromCommand)
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
