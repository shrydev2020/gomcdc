package compileraware

import (
	"context"
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

func TestPrepareRejectsUnsupportedGoVersionBeforeReadingCompilerSources(t *testing.T) {
	for _, version := range []string{"go1.25.9", "go1.27.0"} {
		t.Run(version, func(t *testing.T) {
			fakeBin := t.TempDir()
			fakeGo := filepath.Join(fakeBin, "go")
			script := "#!/bin/sh\nprintf '%s\\n' '/missing/goroot' '" + version + "'\n"
			if err := os.WriteFile(fakeGo, []byte(script), 0o700); err != nil {
				t.Fatalf("write fake go command: %v", err)
			}
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

			_, err := Prepare(context.Background(), t.TempDir())
			want := "requires Go 1.26.x, got " + version
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Prepare error = %v, want %q", err, want)
			}
		})
	}
}

func TestPatchInstalledGo126SwitchLowering(t *testing.T) {
	command := exec.Command("go", "env", "GOROOT", "GOVERSION")
	command.Env = buildEnvironment(os.Environ())
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "go1.26.") {
		t.Skipf("installed toolchain is not Go 1.26.x: %q", strings.TrimSpace(string(output)))
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
