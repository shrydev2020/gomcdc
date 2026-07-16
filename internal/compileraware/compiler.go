// Package compileraware prepares the Go 1.26 compiler producer used for exact
// switch dispatch evidence. It patches only the switch-lowering pass in a
// disposable overlay; the installed GOROOT is never modified.
package compileraware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/internal/processgroup"
)

const compilerEnvironment = "GOMCDC_COMPILER"
const supportedGoSeries = "go1.26"

// Toolchain is the measurement-owned toolexec configuration.
type Toolchain struct {
	Toolexec    string
	Environment map[string]string
}

// Prepare builds a compiler from the exact go command selected on PATH and a
// toolexec shim which substitutes that compiler for compile invocations only.
func Prepare(ctx context.Context, root string) (Toolchain, error) {
	if root == "" {
		return Toolchain{}, errors.New("compiler-aware tool directory is empty")
	}
	goroot, version, err := queryToolchain(ctx)
	if err != nil {
		return Toolchain{}, err
	}
	if !isSupportedGoVersion(version) {
		return Toolchain{}, fmt.Errorf("compiler-aware clause selection requires stable Go 1.26.x, got %s", version)
	}

	realSwitchPath := filepath.Join(goroot, "src", "cmd", "compile", "internal", "walk", "switch.go")
	source, err := readFile(ctx, realSwitchPath)
	if err != nil {
		return Toolchain{}, fmt.Errorf("read Go %s switch lowering source: %w", version, err)
	}
	patched, err := PatchSwitchSource(source)
	if err != nil {
		return Toolchain{}, fmt.Errorf("Go %s compiler source is incompatible with the clause-selection producer: %w", version, err)
	}

	toolDir := filepath.Join(root, "compiler-aware")
	if err := filesystemEffect(ctx, func() error { return os.MkdirAll(toolDir, 0o700) }); err != nil {
		return Toolchain{}, fmt.Errorf("create compiler-aware tool directory: %w", err)
	}
	// Downloaded toolchains live below GOMODCACHE, where cmd/go deliberately
	// rejects overlay replacements. A disposable lexical GOROOT view keeps all
	// installed files read-only through symlinks while giving the overlay a
	// target outside GOMODCACHE.
	shadowGOROOT := filepath.Join(toolDir, "goroot")
	if err := createGOROOTView(ctx, goroot, shadowGOROOT); err != nil {
		return Toolchain{}, err
	}
	switchPath := filepath.Join(shadowGOROOT, "src", "cmd", "compile", "internal", "walk", "switch.go")
	patchedPath := filepath.Join(toolDir, "switch.go")
	if err := filesystemEffect(ctx, func() error { return os.WriteFile(patchedPath, patched, 0o600) }); err != nil {
		return Toolchain{}, fmt.Errorf("write patched switch lowering source: %w", err)
	}
	overlayPath := filepath.Join(toolDir, "overlay.json")
	overlay, err := json.Marshal(struct {
		Replace map[string]string `json:"Replace"`
	}{Replace: map[string]string{switchPath: patchedPath}})
	if err != nil {
		return Toolchain{}, fmt.Errorf("encode compiler overlay: %w", err)
	}
	if err := filesystemEffect(ctx, func() error { return os.WriteFile(overlayPath, overlay, 0o600) }); err != nil {
		return Toolchain{}, fmt.Errorf("write compiler overlay: %w", err)
	}

	compilerPath := filepath.Join(toolDir, "compile")
	selectedGo := filepath.Join(goroot, "bin", "go")
	command := exec.CommandContext(ctx, selectedGo, "build", "-overlay="+overlayPath, "-o="+compilerPath, "cmd/compile")
	processgroup.ConfigureCancellation(command)
	command.Dir = toolDir
	command.Env = buildEnvironment(os.Environ())
	command.Env = setEnvironment(command.Env, "GOROOT", shadowGOROOT)
	output, err := command.CombinedOutput()
	if err != nil {
		return Toolchain{}, fmt.Errorf("build compiler-aware Go %s compiler: %w: %s", version, err, strings.TrimSpace(string(output)))
	}
	if err := filesystemEffect(ctx, func() error { return os.Chmod(compilerPath, 0o700) }); err != nil {
		return Toolchain{}, fmt.Errorf("set compiler executable mode: %w", err)
	}

	toolexecPath := filepath.Join(toolDir, "toolexec")
	patchID := fmt.Sprintf("%x", sha256.Sum256(patched))[:16]
	shim := fmt.Sprintf(`#!/bin/sh
tool=$1
shift
case "$tool" in
	*/compile|compile)
		if [ "$1" = "-V=full" ]; then
			printf '%%s\n' 'compile version %s gomcdc-%s'
			exit 0
		fi
		exec "$GOMCDC_COMPILER" "$@"
		;;
	*) exec "$tool" "$@" ;;
esac
`, version, patchID)
	if err := filesystemEffect(ctx, func() error { return os.WriteFile(toolexecPath, []byte(shim), 0o700) }); err != nil {
		return Toolchain{}, fmt.Errorf("write compiler-aware toolexec shim: %w", err)
	}
	return Toolchain{
		Toolexec:    toolexecPath,
		Environment: map[string]string{compilerEnvironment: compilerPath},
	}, nil
}

func isSupportedGoVersion(version string) bool {
	prefix := supportedGoSeries + "."
	if !strings.HasPrefix(version, prefix) {
		return false
	}
	patch := strings.TrimPrefix(version, prefix)
	if patch == "" {
		return false
	}
	_, err := strconv.ParseUint(patch, 10, 64)
	return err == nil
}

func readFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// filesystemEffect keeps each compiler-preparation mutation inside the
// request's cancellation boundary without duplicating phase checks.
func filesystemEffect(ctx context.Context, effect func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return effect()
}

func createGOROOTView(ctx context.Context, source, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("create compiler GOROOT view: %w", err)
	}
	// cmd/go does not discover standard packages through a symlinked src
	// directory on every supported installation layout. Mirror directories as
	// real directories and symlink only files, so package discovery and overlay
	// path identity both use the disposable lexical GOROOT.
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("walk selected GOROOT at %q: %w", path, walkErr)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("resolve selected GOROOT entry %q: %w", path, err)
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			if err := os.Mkdir(target, 0o700); err != nil {
				return fmt.Errorf("create compiler GOROOT directory %q: %w", relative, err)
			}
			return nil
		}
		if err := os.Symlink(path, target); err != nil {
			return fmt.Errorf("link compiler GOROOT entry %q: %w", relative, err)
		}
		return nil
	})
}

func queryToolchain(ctx context.Context) (string, string, error) {
	command := exec.CommandContext(ctx, "go", "env", "GOROOT", "GOVERSION")
	processgroup.ConfigureCancellation(command)
	command.Env = buildEnvironment(os.Environ())
	output, err := command.Output()
	if err != nil {
		return "", "", fmt.Errorf("query Go toolchain for compiler-aware measurement: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return "", "", fmt.Errorf("query Go toolchain returned %q", strings.TrimSpace(string(output)))
	}
	return filepath.Clean(lines[0]), strings.TrimSpace(lines[1]), nil
}

func buildEnvironment(environment []string) []string {
	result := append([]string(nil), environment...)
	result = setEnvironment(result, "GOFLAGS", "")
	result = setEnvironment(result, "GOWORK", "off")
	result = setEnvironment(result, "GOOS", runtime.GOOS)
	result = setEnvironment(result, "GOARCH", runtime.GOARCH)
	return result
}

func setEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	for index, item := range environment {
		if strings.HasPrefix(item, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

// PatchSwitchSource adds dispatch-only probe trampolines to Go 1.26's switch
// lowering. Exact anchors intentionally fail closed when the compiler source
// changes instead of silently advertising evidence the producer cannot make.
func PatchSwitchSource(source []byte) ([]byte, error) {
	patched := append([]byte(nil), source...)
	replacements := []struct {
		name string
		old  string
		new  string
	}{
		{
			name: "expression selection list",
			old:  "\tvar defaultGoto ir.Node\n\tvar body ir.Nodes\n",
			new:  "\tvar defaultGoto ir.Node\n\tvar selection, body ir.Nodes\n",
		},
		{
			name: "expression body label",
			old:  "\tfor _, ncase := range sw.Cases {\n\t\tlabel := typecheck.AutoLabel(\".s\")\n\t\tjmp := ir.NewBranchStmt(ncase.Pos(), ir.OGOTO, label)\n",
			new:  "\tfor _, ncase := range sw.Cases {\n\t\tbodyLabel := typecheck.AutoLabel(\".s\")\n\t\tprobe := gomcdcSelectionProbe(ncase)\n",
		},
		{
			name: "expression default jump",
			old:  "\t\t// Process case dispatch.\n\t\tif len(ncase.List) == 0 {\n\t\t\tif defaultGoto != nil {\n\t\t\t\tbase.Fatalf(\"duplicate default case not detected during typechecking\")\n\t\t\t}\n\t\t\tdefaultGoto = jmp\n\t\t}\n",
			new:  "\t\t// Process case dispatch.\n\t\tif len(ncase.List) == 0 {\n\t\t\tif defaultGoto != nil {\n\t\t\t\tbase.Fatalf(\"duplicate default case not detected during typechecking\")\n\t\t\t}\n\t\t\tdefaultGoto = gomcdcSelectionJump(ncase.Pos(), bodyLabel, probe, -1, &selection)\n\t\t}\n",
		},
		{
			name: "expression alternative jump",
			old:  "\t\t\ts.Add(ncase.Pos(), n1, rtype, jmp)\n",
			new:  "\t\t\tjmp := gomcdcSelectionJump(ncase.Pos(), bodyLabel, probe, i, &selection)\n\t\t\ts.Add(ncase.Pos(), n1, rtype, jmp)\n",
		},
		{
			name: "expression body",
			old:  "\t\tbody.Append(ir.NewLabelStmt(ncase.Pos(), label))\n",
			new:  "\t\tbody.Append(ir.NewLabelStmt(ncase.Pos(), bodyLabel))\n",
		},
		{
			name: "expression compiled selection",
			old:  "\tsw.Compiled.Append(defaultGoto)\n\tsw.Compiled.Append(body.Take()...)\n",
			new:  "\tsw.Compiled.Append(defaultGoto)\n\tsw.Compiled.Append(selection.Take()...)\n\tsw.Compiled.Append(body.Take()...)\n",
		},
		{
			name: "type selection setup",
			old:  "\tlabels := make([]*types.Sym, len(sw.Cases))\n\tfor i := range sw.Cases {\n\t\tlabels[i] = typecheck.AutoLabel(\".s\")\n\t}\n",
			new:  "\tlabels := make([]*types.Sym, len(sw.Cases))\n\tprobes := make([]ir.Node, len(sw.Cases))\n\tvar selection ir.Nodes\n\tfor i, ncase := range sw.Cases {\n\t\tlabels[i] = typecheck.AutoLabel(\".s\")\n\t\tprobes[i] = gomcdcSelectionProbe(ncase)\n\t}\n",
		},
		{
			name: "type shared jump",
			old:  "\tfor i, ncase := range sw.Cases {\n\t\tjmp := ir.NewBranchStmt(ncase.Pos(), ir.OGOTO, labels[i])\n",
			new:  "\tfor i, ncase := range sw.Cases {\n",
		},
		{
			name: "type default jump",
			old:  "\t\tif len(ncase.List) == 0 { // default:\n\t\t\tif defaultGoto != nil {\n\t\t\t\tbase.Fatalf(\"duplicate default case not detected during typechecking\")\n\t\t\t}\n\t\t\tdefaultGoto = jmp\n\t\t}\n",
			new:  "\t\tif len(ncase.List) == 0 { // default:\n\t\t\tif defaultGoto != nil {\n\t\t\t\tbase.Fatalf(\"duplicate default case not detected during typechecking\")\n\t\t\t}\n\t\t\tdefaultGoto = gomcdcSelectionJump(ncase.Pos(), labels[i], probes[i], -1, &selection)\n\t\t}\n",
		},
		{
			name: "type alternative loop",
			old:  "\t\tfor _, n1 := range ncase.List {\n",
			new:  "\t\tfor alternative, n1 := range ncase.List {\n\t\t\tjmp := gomcdcSelectionJump(ncase.Pos(), labels[i], probes[i], alternative, &selection)\n",
		},
		{
			name: "type compiled selection",
			old:  "\tsw.Compiled.Append(defaultGoto) // if none of the cases matched\n\n\t// Now generate all the case bodies\n",
			new:  "\tsw.Compiled.Append(defaultGoto) // if none of the cases matched\n\tsw.Compiled.Append(selection.Take()...)\n\n\t// Now generate all the case bodies\n",
		},
	}
	for _, replacement := range replacements {
		var err error
		patched, err = replaceExactlyOnce(patched, replacement.old, replacement.new)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", replacement.name, err)
		}
	}
	patched = append(patched, []byte(`

// gomcdcSelectionProbe removes a generated marker from a source case body.
// The compiler is used only for gomcdc's instrumented workspace. Markers must
// resolve to the fresh injected runtime package as well as the exact noinline
// method, so similarly named user methods retain ordinary source semantics.
func gomcdcSelectionProbe(ncase *ir.CaseClause) ir.Node {
	if len(ncase.Body) == 0 {
		return nil
	}
	probe := ncase.Body[0]
	call, ok := probe.(*ir.CallExpr)
	if !ok || call.Fun == nil {
		return nil
	}
	callee := ir.StaticCalleeName(call.Fun)
	if callee == nil || callee.Sym() == nil || callee.Sym().Pkg == nil ||
		!strings.Contains(callee.Sym().Pkg.Path, "/internal/gomcdc_runtime_") {
		return nil
	}
	name := callee.Sym().Name
	switch {
	case strings.HasSuffix(name, ".CompilerDirectClause"):
		if len(call.Args) != 4 {
			return nil
		}
	case strings.HasSuffix(name, ".CompilerNoMatch"):
		if len(call.Args) != 2 {
			return nil
		}
	default:
		return nil
	}
	ncase.Body = ncase.Body[1:]
	return probe
}

// gomcdcSelectionJump targets a trampoline that records dispatch before
// entering the source body. Fallthrough reaches body labels directly and
// therefore bypasses every trampoline.
func gomcdcSelectionJump(pos src.XPos, bodyLabel *types.Sym, probe ir.Node, alternative int, out *ir.Nodes) ir.Node {
	if probe == nil {
		return ir.NewBranchStmt(pos, ir.OGOTO, bodyLabel)
	}
	selected := probe
	if alternative >= 0 {
		call := ir.Copy(probe).(*ir.CallExpr)
		call.Args = call.Args.Copy()
		index := ir.NewInt(probe.Pos(), int64(alternative))
		call.Args[len(call.Args)-1] = typecheck.DefaultLit(index, types.Types[types.TUINT64])
		selected = call
	}
	selectionLabel := typecheck.AutoLabel(".s")
	out.Append(ir.NewLabelStmt(pos, selectionLabel))
	out.Append(selected)
	out.Append(ir.NewBranchStmt(pos, ir.OGOTO, bodyLabel))
	return ir.NewBranchStmt(pos, ir.OGOTO, selectionLabel)
}
`)...)
	return patched, nil
}

func replaceExactlyOnce(source []byte, old, replacement string) ([]byte, error) {
	count := bytes.Count(source, []byte(old))
	if count != 1 {
		return nil, fmt.Errorf("anchor count is %d, want 1", count)
	}
	return bytes.Replace(source, []byte(old), []byte(replacement), 1), nil
}
