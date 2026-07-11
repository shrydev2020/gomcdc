package loader

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadModulePackages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeLoaderFile(t, filepath.Join(root, "go.mod"), "module example.test/fixture\n\ngo 1.26\n")
	writeLoaderFile(t, filepath.Join(root, "alpha", "alpha.go"), "package alpha\nfunc Value() bool { return true }\n")
	writeLoaderFile(t, filepath.Join(root, "alpha", "alpha_test.go"), "package alpha\n")
	writeLoaderFile(t, filepath.Join(root, "beta", "beta.go"), "package beta\n")

	withoutTests, err := Load(context.Background(), Options{Dir: root, Patterns: []string{"./..."}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if withoutTests.ModulePath != "example.test/fixture" || withoutTests.ModuleRoot != root {
		t.Fatalf("unexpected module: %#v", withoutTests)
	}
	if got := baseNames(withoutTests.Files); strings.Contains(got, "alpha_test.go") {
		t.Fatalf("files include test without IncludeTests: %s", got)
	}
	if got := len(withoutTests.PackageImportSet); got != 2 {
		t.Fatalf("package count = %d, want 2", got)
	}

	withTests, err := Load(context.Background(), Options{Dir: root, Patterns: []string{"./..."}, IncludeTests: true})
	if err != nil {
		t.Fatalf("Load(include tests) error = %v", err)
	}
	if got := baseNames(withTests.Files); !strings.Contains(got, "alpha_test.go") {
		t.Fatalf("files do not include active test: %s", got)
	}
}

func TestLoadHonorsBuildTags(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeLoaderFile(t, filepath.Join(root, "go.mod"), "module example.test/tags\n\ngo 1.26\n")
	writeLoaderFile(t, filepath.Join(root, "base.go"), "package tags\n")
	writeLoaderFile(t, filepath.Join(root, "custom.go"), "//go:build customtag\n\npackage tags\n")
	writeLoaderFile(t, filepath.Join(root, "other.go"), "//go:build !customtag\n\npackage tags\n")

	result, err := Load(context.Background(), Options{
		Dir: root, Patterns: []string{"."}, BuildFlags: []string{"-tags=customtag"},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	names := baseNames(result.Files)
	if !strings.Contains(names, "custom.go") || strings.Contains(names, "other.go") {
		t.Fatalf("active files = %s, GOOS=%s", names, runtime.GOOS)
	}
}

func TestLoadRejectsActiveMultiModuleWorkspace(t *testing.T) {
	// The fixture must test filesystem workspace discovery, not an inherited
	// caller policy such as GOWORK=off.
	t.Setenv("GOWORK", "")
	root := t.TempDir()
	module := filepath.Join(root, "module")
	writeLoaderFile(t, filepath.Join(module, "go.mod"), "module example.test/workmodule\n\ngo 1.26\n")
	writeLoaderFile(t, filepath.Join(module, "value.go"), "package workmodule\n")
	writeLoaderFile(t, filepath.Join(root, "go.work"), "go 1.26\n\nuse ./module\n")
	_, err := Load(context.Background(), Options{Dir: module, Patterns: []string{"."}})
	if err == nil || !strings.Contains(err.Error(), "active go.work") {
		t.Fatalf("Load() error = %v, want explicit go.work rejection", err)
	}
}

func TestLoadDoesNotExposeSyntheticExternalTestPackageAsTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeLoaderFile(t, filepath.Join(root, "go.mod"), "module example.test/external\n\ngo 1.26\n")
	writeLoaderFile(t, filepath.Join(root, "value.go"), "package external\n")
	writeLoaderFile(t, filepath.Join(root, "value_test.go"), "package external_test\n")
	result, err := Load(context.Background(), Options{Dir: root, Patterns: []string{"."}, IncludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(result.PackageImportSet, ","), "example.test/external"; got != want {
		t.Fatalf("target packages = %q, want %q", got, want)
	}
	if got := baseNames(result.Files); !strings.Contains(got, "value_test.go") {
		t.Fatalf("instrumentable files = %q, want external test file", got)
	}
}

func TestLoadRetainsProductionFilesWithTypeErrorsForPartialGoTestRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeLoaderFile(t, filepath.Join(root, "go.mod"), "module example.test/broken\n\ngo 1.26\n")
	writeLoaderFile(t, filepath.Join(root, "broken.go"), "package broken\nvar Value string = 42\n")
	result, err := Load(context.Background(), Options{Dir: root, Patterns: []string{"."}})
	if err != nil {
		t.Fatalf("Load() error = %v; go test must own the build failure", err)
	}
	if got := baseNames(result.Files); got != "broken.go" {
		t.Fatalf("files = %q, want broken.go retained", got)
	}
}

func baseNames(files []File) string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, filepath.Base(file.Path))
	}
	return strings.Join(names, ",")
}

func writeLoaderFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
