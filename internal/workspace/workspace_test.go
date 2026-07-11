package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreateCopiesModuleTree(t *testing.T) {
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n", 0o640)
	writeFile(t, filepath.Join(source, "assets", "message.txt"), "non-Go asset\n", 0o644)
	executable := filepath.Join(source, "scripts", "run.sh")
	writeFile(t, executable, "#!/bin/sh\n", 0o751)
	writeFile(t, filepath.Join(source, ".git", "config"), "must not be copied\n", 0o600)
	writeFile(t, filepath.Join(source, "nested", ".git", "HEAD"), "must not be copied\n", 0o600)

	if err := os.Symlink(filepath.Join("..", "assets", "message.txt"), filepath.Join(source, "scripts", "message-link")); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}

	workspace, err := Create(Options{SourceDir: source, TempParent: t.TempDir()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() {
		if err := workspace.Remove(); err != nil {
			t.Errorf("Remove() error = %v", err)
		}
	})

	if workspace.SourceDir != canonicalPath(t, source) {
		t.Errorf("SourceDir = %q, want %q", workspace.SourceDir, canonicalPath(t, source))
	}
	if filepath.Dir(workspace.ModuleDir) != workspace.RootDir {
		t.Errorf("ModuleDir %q is not directly under RootDir %q", workspace.ModuleDir, workspace.RootDir)
	}
	if filepath.Dir(workspace.EventDir) != workspace.RootDir {
		t.Errorf("EventDir %q is not directly under RootDir %q", workspace.EventDir, workspace.RootDir)
	}
	if workspace.EventDir == workspace.ModuleDir {
		t.Fatal("event and module directories must be separate")
	}

	asset, err := os.ReadFile(filepath.Join(workspace.ModuleDir, "assets", "message.txt"))
	if err != nil {
		t.Fatalf("read copied asset: %v", err)
	}
	if got, want := string(asset), "non-Go asset\n"; got != want {
		t.Errorf("copied asset = %q, want %q", got, want)
	}
	if _, err := os.Lstat(filepath.Join(workspace.ModuleDir, ".git")); !os.IsNotExist(err) {
		t.Errorf("root .git entry was copied; error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace.ModuleDir, "nested", ".git")); !os.IsNotExist(err) {
		t.Errorf("nested .git entry was copied; error = %v", err)
	}

	link := filepath.Join(workspace.ModuleDir, "scripts", "message-link")
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("inspect copied symlink: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("copied link mode = %v, want symlink", linkInfo.Mode())
	}
	if got, err := os.Readlink(link); err != nil {
		t.Fatalf("read copied symlink: %v", err)
	} else if want := filepath.Join("..", "assets", "message.txt"); got != want {
		t.Errorf("copied symlink target = %q, want %q", got, want)
	}

	// Windows does not expose Unix permission bits with the same semantics.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(workspace.ModuleDir, "scripts", "run.sh"))
		if err != nil {
			t.Fatalf("inspect copied executable: %v", err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o751); got != want {
			t.Errorf("copied executable mode = %v, want %v", got, want)
		}
	}

	entries, err := os.ReadDir(workspace.EventDir)
	if err != nil {
		t.Fatalf("read event directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("new event directory contains %d entries, want 0", len(entries))
	}
}

func TestCreateRewritesRelativeModuleReplacementsOnlyInCopy(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	dependency := filepath.Join(parent, "dependency")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dependency, 0o755); err != nil {
		t.Fatal(err)
	}
	original := "module example.test/source\n\ngo 1.24\n\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => ../dependency\n"
	writeFile(t, filepath.Join(source, "go.mod"), original, 0o644)
	writeFile(t, filepath.Join(dependency, "go.mod"), "module example.test/dependency\n\ngo 1.24\n", 0o644)

	work, err := Create(Options{SourceDir: source, TempParent: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = work.Remove() })
	copied, err := os.ReadFile(filepath.Join(work.ModuleDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(copied), "=> "+canonicalPath(t, dependency)) {
		t.Fatalf("copied go.mod did not canonicalize replacement:\n%s", copied)
	}
	unchanged, err := os.ReadFile(filepath.Join(source, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != original {
		t.Fatalf("original go.mod changed:\n%s", unchanged)
	}
}

func TestCreateRejectsTempParentInsideSourceTree(t *testing.T) {
	source := t.TempDir()
	inside := filepath.Join(source, "tmp")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("create inner temp parent: %v", err)
	}

	for _, parent := range []string{source, inside} {
		t.Run(filepath.Base(parent), func(t *testing.T) {
			if _, err := Create(Options{SourceDir: source, TempParent: parent}); err == nil {
				t.Fatal("Create() error = nil, want containment error")
			}
		})
	}
}

func TestCreateRejectsSymlinkedTempParentInsideSourceTree(t *testing.T) {
	source := t.TempDir()
	inside := filepath.Join(source, "tmp")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("create inner temp parent: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "source-tmp")
	if err := os.Symlink(inside, alias); err != nil {
		t.Fatalf("create temp parent symlink: %v", err)
	}

	if _, err := Create(Options{SourceDir: source, TempParent: alias}); err == nil {
		t.Fatal("Create() error = nil, want containment error through symlink")
	}
}

func TestWorkspaceCleanupAndKeep(t *testing.T) {
	newWorkspace := func(t *testing.T, keep bool) *Workspace {
		t.Helper()
		source := t.TempDir()
		writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n", 0o644)
		workspace, err := Create(Options{SourceDir: source, TempParent: t.TempDir(), Keep: keep})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		return workspace
	}

	t.Run("cleanup removes by default", func(t *testing.T) {
		workspace := newWorkspace(t, false)
		if err := workspace.Cleanup(); err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
		if _, err := os.Stat(workspace.RootDir); !os.IsNotExist(err) {
			t.Errorf("workspace still exists after Cleanup; error = %v", err)
		}
		if err := workspace.Cleanup(); err != nil {
			t.Errorf("second Cleanup() error = %v", err)
		}
	})

	t.Run("option retains and remove overrides", func(t *testing.T) {
		workspace := newWorkspace(t, true)
		if !workspace.IsKept() {
			t.Fatal("IsKept() = false, want true")
		}
		if err := workspace.Cleanup(); err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
		if _, err := os.Stat(workspace.RootDir); err != nil {
			t.Fatalf("retained workspace missing after Cleanup: %v", err)
		}
		if err := workspace.Remove(); err != nil {
			t.Fatalf("Remove() error = %v", err)
		}
		if _, err := os.Stat(workspace.RootDir); !os.IsNotExist(err) {
			t.Errorf("retained workspace still exists after Remove; error = %v", err)
		}
	})

	t.Run("keep method retains", func(t *testing.T) {
		workspace := newWorkspace(t, false)
		workspace.Keep()
		if !workspace.IsKept() {
			t.Fatal("IsKept() = false after Keep, want true")
		}
		if err := workspace.Cleanup(); err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
		if _, err := os.Stat(workspace.RootDir); err != nil {
			t.Fatalf("workspace missing after Keep and Cleanup: %v", err)
		}
		if err := workspace.Remove(); err != nil {
			t.Fatalf("Remove() error = %v", err)
		}
	})
}

func TestCreateValidatesDirectories(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "not a directory", 0o644)

	tests := []Options{
		{},
		{SourceDir: file},
		{SourceDir: t.TempDir(), TempParent: file},
	}
	for index, options := range tests {
		if _, err := Create(options); err == nil {
			t.Errorf("Create(test %d) error = nil, want validation error", index)
		}
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %q: %v", path, err)
	}
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return filepath.Clean(canonical)
}
