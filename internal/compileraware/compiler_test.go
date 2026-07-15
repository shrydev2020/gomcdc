package compileraware

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareBuildsCompilerAwareToolchain(t *testing.T) {
	toolchain, err := Prepare(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if toolchain.Toolexec == "" || toolchain.Environment[compilerEnvironment] == "" {
		t.Fatalf("incomplete toolchain: %#v", toolchain)
	}
	info, err := os.Stat(toolchain.Toolexec)
	if err != nil {
		t.Fatalf("stat toolexec: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("toolexec is not executable: %v", info.Mode())
	}
	version := exec.Command(toolchain.Toolexec, "/unused/compile", "-V=full")
	output, err := version.Output()
	if err != nil {
		t.Fatalf("toolexec compiler ID: %v", err)
	}
	if text := string(output); !strings.Contains(text, " gomcdc-") {
		t.Fatalf("compiler ID does not isolate the build cache: %q", text)
	}
}

func TestCreateGOROOTViewRejectsCanceledWork(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(t.TempDir(), "goroot")
	if err := createGOROOTView(ctx, t.TempDir(), destination); !errors.Is(err, context.Canceled) {
		t.Fatalf("createGOROOTView error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled GOROOT view was created: %v", err)
	}
}

func TestFilesystemEffectHonorsCancellation(t *testing.T) {
	t.Parallel()

	called := false
	if err := filesystemEffect(t.Context(), func() error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("active filesystem effect: called=%v err=%v", called, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called = false
	if err := filesystemEffect(ctx, func() error {
		called = true
		return nil
	}); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled filesystem effect: called=%v err=%v", called, err)
	}
}

func TestReadFileHonorsCancellation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "value.txt")
	if err := os.WriteFile(path, []byte("value"), 0o644); err != nil {
		t.Fatal(err)
	}
	contents, err := readFile(t.Context(), path)
	if err != nil || string(contents) != "value" {
		t.Fatalf("active read: contents=%q err=%v", contents, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readFile(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read error = %v, want context.Canceled", err)
	}
}

func TestPrepareRejectsUnsupportedGoVersionBeforeReadingCompilerSources(t *testing.T) {
	for _, version := range []string{"go1.25.9", "go1.26.4", "go1.26.6", "go1.27.0"} {
		t.Run(version, func(t *testing.T) {
			fakeBin := t.TempDir()
			fakeGo := filepath.Join(fakeBin, "go")
			script := "#!/bin/sh\nprintf '%s\\n' '/missing/goroot' '" + version + "'\n"
			if err := os.WriteFile(fakeGo, []byte(script), 0o700); err != nil {
				t.Fatalf("write fake go command: %v", err)
			}
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

			_, err := Prepare(context.Background(), t.TempDir())
			want := "requires " + supportedGoVersion + ", got " + version
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Prepare error = %v, want %q", err, want)
			}
		})
	}
}

func TestPrepareFailsClosedForInvalidSetup(t *testing.T) {
	t.Run("empty tool root", func(t *testing.T) {
		if _, err := Prepare(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "tool directory is empty") {
			t.Fatalf("Prepare error = %v", err)
		}
	})

	t.Run("go env failure", func(t *testing.T) {
		fakeBin := t.TempDir()
		fakeGo := filepath.Join(fakeBin, "go")
		if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", fakeBin)
		if _, err := Prepare(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "query Go toolchain") {
			t.Fatalf("Prepare error = %v", err)
		}
	})

	t.Run("missing compiler source", func(t *testing.T) {
		goroot := t.TempDir()
		setFakeGoEnv(t, goroot, supportedGoVersion)
		if _, err := Prepare(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "read Go "+supportedGoVersion+" switch lowering source") {
			t.Fatalf("Prepare error = %v", err)
		}
	})

	t.Run("incompatible compiler source", func(t *testing.T) {
		goroot := t.TempDir()
		switchDir := filepath.Join(goroot, "src", "cmd", "compile", "internal", "walk")
		if err := os.MkdirAll(switchDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(switchDir, "switch.go"), []byte("package walk\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		setFakeGoEnv(t, goroot, supportedGoVersion)
		if _, err := Prepare(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "compiler source is incompatible") {
			t.Fatalf("Prepare error = %v", err)
		}
	})

	t.Run("tool root is a file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "tool-root")
		if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Prepare(context.Background(), root); err == nil || !strings.Contains(err.Error(), "create compiler-aware tool directory") {
			t.Fatalf("Prepare error = %v", err)
		}
	})
}

func setFakeGoEnv(t *testing.T, goroot, version string) {
	t.Helper()
	fakeBin := t.TempDir()
	fakeGo := filepath.Join(fakeBin, "go")
	script := "#!/bin/sh\nprintf '%s\\n' '" + goroot + "' '" + version + "'\n"
	if err := os.WriteFile(fakeGo, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
}

func TestPatchInstalledGo1265SwitchLowering(t *testing.T) {
	command := exec.Command("go", "env", "GOROOT", "GOVERSION")
	command.Env = buildEnvironment(os.Environ())
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || lines[1] != supportedGoVersion {
		t.Skipf("installed toolchain is not %s: %q", supportedGoVersion, strings.TrimSpace(string(output)))
	}
	source, err := os.ReadFile(filepath.Join(lines[0], "src", "cmd", "compile", "internal", "walk", "switch.go"))
	if err != nil {
		t.Fatalf("read switch.go: %v", err)
	}
	patched, err := PatchSwitchSource(source)
	if err != nil {
		t.Fatalf("PatchSwitchSource: %v", err)
	}
	for _, required := range []string{"gomcdcSelectionProbe", "gomcdcSelectionJump", "selection.Take()"} {
		if !strings.Contains(string(patched), required) {
			t.Errorf("patched compiler source lacks %q", required)
		}
	}
}

func TestPatchSwitchSourceFailsClosed(t *testing.T) {
	if _, err := PatchSwitchSource([]byte("package walk\n")); err == nil {
		t.Fatal("incompatible compiler source was accepted")
	}
}
