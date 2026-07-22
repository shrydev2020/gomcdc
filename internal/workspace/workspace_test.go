package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/modulecontext"
)

func TestCreateRejectsCanceledWork(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Create(ctx, Options{TempParent: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want context cancellation", err)
	}
}

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
	if err := os.Symlink(filepath.Join(source, "assets", "message.txt"), filepath.Join(source, "scripts", "absolute-message-link")); err != nil {
		t.Fatalf("create absolute source symlink: %v", err)
	}
	if err := os.Link(filepath.Join(source, "assets", "message.txt"), filepath.Join(source, "assets", "message-hardlink")); err != nil {
		t.Fatalf("create source hardlink: %v", err)
	}

	workspace, err := Create(t.Context(), Options{SourceConfiguration: snapshotModule(t, source), TempParent: t.TempDir()})
	if err != nil {
		t.Fatalf("Create(t.Context(), ) error = %v", err)
	}
	t.Cleanup(func() {
		if err := workspace.Remove(); err != nil {
			t.Errorf("Remove() error = %v", err)
		}
	})

	if workspace.SourceModuleDir != canonicalPath(t, source) {
		t.Errorf("SourceModuleDir = %q, want %q", workspace.SourceModuleDir, canonicalPath(t, source))
	}
	if workspace.ModuleDir != workspace.MappedRootDir || workspace.WorkspaceDir != workspace.ModuleDir {
		t.Errorf("standalone mapping = root %q workspace %q module %q", workspace.MappedRootDir, workspace.WorkspaceDir, workspace.ModuleDir)
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
	absoluteLink := filepath.Join(workspace.ModuleDir, "scripts", "absolute-message-link")
	if err := os.WriteFile(absoluteLink, []byte("copy only\n"), 0o644); err != nil {
		t.Fatalf("write through relocated absolute symlink: %v", err)
	}
	if original, err := os.ReadFile(filepath.Join(source, "assets", "message.txt")); err != nil {
		t.Fatal(err)
	} else if string(original) != "non-Go asset\n" {
		t.Fatalf("write through copied symlink changed original source: %q", original)
	}

	copiedHardlink := filepath.Join(workspace.ModuleDir, "assets", "message-hardlink")
	sourceHardlinkInfo, err := os.Stat(filepath.Join(source, "assets", "message-hardlink"))
	if err != nil {
		t.Fatal(err)
	}
	copiedHardlinkInfo, err := os.Stat(copiedHardlink)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceHardlinkInfo, copiedHardlinkInfo) {
		t.Fatal("copied regular file retained a hardlink to the source tree")
	}
	if err := os.WriteFile(copiedHardlink, []byte("copy hardlink only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if original, err := os.ReadFile(filepath.Join(source, "assets", "message-hardlink")); err != nil {
		t.Fatal(err)
	} else if string(original) != "non-Go asset\n" {
		t.Fatalf("write to copied former hardlink changed original source: %q", original)
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

func TestCreateRejectsSymlinkThatResolvesOutsideSourceTree(t *testing.T) {
	source := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n", 0o644)
	writeFile(t, outside, "outside\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(source, "outside-link")); err != nil {
		t.Fatal(err)
	}

	_, err := Create(t.Context(), Options{SourceConfiguration: snapshotModule(t, source), TempParent: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "resolves outside source tree") {
		t.Fatalf("Create() error = %v, want outside-symlink rejection", err)
	}
	if contents, readErr := os.ReadFile(outside); readErr != nil {
		t.Fatal(readErr)
	} else if string(contents) != "outside\n" {
		t.Fatalf("outside file changed: %q", contents)
	}
}

func TestCreateRewritesSingleModuleGoWorkForCopiedModule(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "module")
	dependency := filepath.Join(root, "dependency")
	writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n\ngo 1.26\n\nrequire example.test/dependency v0.0.0\n", 0o644)
	writeFile(t, filepath.Join(source, "source.go"), "package source\n", 0o644)
	writeFile(t, filepath.Join(dependency, "go.mod"), "module example.test/dependency\n\ngo 1.26\n", 0o644)
	goWork := filepath.Join(root, "go.work")
	writeFile(t, goWork, "go 1.26\n\nuse ./module\n\nreplace example.test/dependency => ./dependency\n", 0o644)
	writeFile(t, goWork+".sum", "example.test/dependency v1.0.0/go.mod h1:test\n", 0o644)
	t.Setenv("GOWORK", goWork)
	settings, err := modulecontext.Discover(t.Context(), modulecontext.DiscoverOptions{Dir: source})
	if err != nil {
		t.Fatal(err)
	}

	work, err := Create(t.Context(), Options{SourceConfiguration: settings, TempParent: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = work.Remove() })
	contents, err := os.ReadFile(work.GoWorkPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, "use ./module") || !strings.Contains(text, "=> "+canonicalPath(t, dependency)) {
		t.Fatalf("rewritten go.work does not preserve the single-module settings:\n%s", text)
	}
	if sum, err := os.ReadFile(work.GoWorkPath + ".sum"); err != nil || string(sum) != "example.test/dependency v1.0.0/go.mod h1:test\n" {
		t.Fatalf("copied go.work.sum = %q, %v", sum, err)
	}
}

func TestCreatePreservesWorkspaceModuleAndInvocationTopology(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspace")
	source := filepath.Join(workspaceRoot, "src", "app")
	invocation := filepath.Join(workspaceRoot, "tools", "runner")
	writeFile(t, filepath.Join(source, "go.mod"), "module example.test/app\n\ngo 1.26\n", 0o644)
	writeFile(t, filepath.Join(source, "app.go"), "package app\n", 0o644)
	if err := os.MkdirAll(invocation, 0o755); err != nil {
		t.Fatal(err)
	}
	goWork := filepath.Join(workspaceRoot, "go.work")
	writeFile(t, goWork, "go 1.26\n\nuse ./src/app\n", 0o644)
	t.Setenv("GOWORK", goWork)
	configuration, err := modulecontext.Discover(t.Context(), modulecontext.DiscoverOptions{Dir: invocation})
	if err != nil {
		t.Fatal(err)
	}

	work, err := Create(t.Context(), Options{
		SourceConfiguration: configuration,
		WorkingDir:          invocation,
		TempParent:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = work.Remove() })
	if relative, err := filepath.Rel(work.WorkspaceDir, work.ModuleDir); err != nil || relative != filepath.Join("src", "app") {
		t.Fatalf("copied module relative path = %q, %v", relative, err)
	}
	if relative, err := filepath.Rel(work.WorkspaceDir, work.WorkingDir); err != nil || relative != filepath.Join("tools", "runner") {
		t.Fatalf("copied working relative path = %q, %v", relative, err)
	}
	contents, err := os.ReadFile(work.GoWorkPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "use ./src/app") {
		t.Fatalf("copied go.work lost module topology:\n%s", contents)
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
	original := "module example.test/source\n\ngo 1.26\n\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => ../dependency\n"
	writeFile(t, filepath.Join(source, "go.mod"), original, 0o644)
	writeFile(t, filepath.Join(dependency, "go.mod"), "module example.test/dependency\n\ngo 1.26\n", 0o644)

	work, err := Create(t.Context(), Options{SourceConfiguration: snapshotModule(t, source), TempParent: t.TempDir()})
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

func TestCreateMaterializesAlternateModFileAndSum(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "module")
	writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n\ngo 1.26\n", 0o644)
	writeFile(t, filepath.Join(source, "dependency", "go.mod"), "module example.test/dependency\n\ngo 1.26\n", 0o644)
	alternate := filepath.Join(root, "config", "analysis.mod")
	writeFile(t, alternate, "module example.test/source\n\ngo 1.26\n\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => ./dependency\n", 0o644)
	writeFile(t, strings.TrimSuffix(alternate, ".mod")+".sum", "sum snapshot\n", 0o644)
	t.Setenv("GOWORK", "off")
	settings, err := modulecontext.Discover(t.Context(), modulecontext.DiscoverOptions{
		Dir: source, BuildFlags: []string{"-modfile=" + alternate},
	})
	if err != nil {
		t.Fatal(err)
	}

	work, err := Create(t.Context(), Options{SourceConfiguration: settings, TempParent: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = work.Remove() })
	if work.ModFilePath == "" || !containsPath(work.RootDir, work.ModFilePath) {
		t.Fatalf("relocated alternate modfile = %q, want inside %q", work.ModFilePath, work.RootDir)
	}
	mod, err := os.ReadFile(work.ModFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mod), "=> "+filepath.Join(work.ModuleDir, "dependency")) {
		t.Fatalf("relocated alternate modfile:\n%s", mod)
	}
	sum, err := os.ReadFile(strings.TrimSuffix(work.ModFilePath, ".mod") + ".sum")
	if err != nil || string(sum) != "sum snapshot\n" {
		t.Fatalf("relocated alternate sum = %q, %v", sum, err)
	}
}

func TestCreateRejectsTempParentInsideSourceTree(t *testing.T) {
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n", 0o644)
	inside := filepath.Join(source, "tmp")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("create inner temp parent: %v", err)
	}

	for _, parent := range []string{source, inside} {
		t.Run(filepath.Base(parent), func(t *testing.T) {
			if _, err := Create(t.Context(), Options{SourceConfiguration: snapshotModule(t, source), TempParent: parent}); err == nil {
				t.Fatal("Create(t.Context(), ) error = nil, want containment error")
			}
		})
	}
}

func TestCreateRejectsSymlinkedTempParentInsideSourceTree(t *testing.T) {
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n", 0o644)
	inside := filepath.Join(source, "tmp")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("create inner temp parent: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "source-tmp")
	if err := os.Symlink(inside, alias); err != nil {
		t.Fatalf("create temp parent symlink: %v", err)
	}

	if _, err := Create(t.Context(), Options{SourceConfiguration: snapshotModule(t, source), TempParent: alias}); err == nil {
		t.Fatal("Create(t.Context(), ) error = nil, want containment error through symlink")
	}
}

func TestWorkspaceCleanupAndKeep(t *testing.T) {
	newWorkspace := func(t *testing.T, keep bool) *Workspace {
		t.Helper()
		source := t.TempDir()
		writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n", 0o644)
		workspace, err := Create(t.Context(), Options{SourceConfiguration: snapshotModule(t, source), TempParent: t.TempDir(), Keep: keep})
		if err != nil {
			t.Fatalf("Create(t.Context(), ) error = %v", err)
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
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "go.mod"), "module example.test/source\n", 0o644)
	configuration := snapshotModule(t, source)

	tests := []Options{
		{},
		{SourceConfiguration: configuration, WorkingDir: file},
		{SourceConfiguration: configuration, TempParent: file},
	}
	for index, options := range tests {
		if _, err := Create(t.Context(), options); err == nil {
			t.Errorf("Create(t.Context(), test %d) error = nil, want validation error", index)
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

func snapshotModule(t *testing.T, source string) modulecontext.SourceConfiguration {
	t.Helper()
	settings, err := modulecontext.SnapshotModule(filepath.Join(source, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return filepath.Clean(canonical)
}
