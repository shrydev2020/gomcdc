// Package modulecontext freezes the Go module settings shared by source
// analysis and the copied test workspace.
package modulecontext

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shrydev2020/gomcdc/internal/processgroup"
	"golang.org/x/mod/modfile"
)

// Settings is the immutable module-mode authority for one gomcdc request. A
// valid value always owns the exact go.mod bytes and, when active, the exact
// go.work bytes observed before package loading.
type Settings struct {
	goWorkPath    string
	goWork        []byte
	goModPath     string
	goMod         []byte
	mainModuleDir string
}

// Discover snapshots the active go.mod and optional go.work, rejecting a
// workspace that does not have exactly one main module. The returned settings
// must be checked against package loading with AssertMainModule.
func Discover(ctx context.Context, dir string) (Settings, error) {
	command := exec.CommandContext(ctx, "go", "env", "GOWORK", "GOMOD")
	processgroup.ConfigureCancellation(command)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		return Settings{}, fmt.Errorf("inspect Go workspace: %w", err)
	}
	values := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	if len(values) != 2 {
		return Settings{}, fmt.Errorf("inspect Go module settings: go env returned %d values", len(values))
	}
	workspacePath := strings.TrimSuffix(values[0], "\r")
	goModPath := strings.TrimSuffix(values[1], "\r")
	if workspacePath == "" || workspacePath == "off" {
		return discoverModuleMode(goModPath)
	}
	absolutePath, err := filepath.Abs(workspacePath)
	if err != nil {
		return Settings{}, fmt.Errorf("resolve active go.work %q: %w", workspacePath, err)
	}
	absolutePath, err = filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return Settings{}, fmt.Errorf("resolve active go.work links %q: %w", workspacePath, err)
	}
	contents, err := os.ReadFile(absolutePath)
	if err != nil {
		return Settings{}, fmt.Errorf("inspect active go.work %q: %w", absolutePath, err)
	}
	parsed, err := modfile.ParseWork(absolutePath, contents, nil)
	if err != nil {
		return Settings{}, fmt.Errorf("inspect active go.work %q: %w", absolutePath, err)
	}
	if len(parsed.Use) != 1 {
		return Settings{}, fmt.Errorf("active go.work %q has %d main modules; exactly one is required", absolutePath, len(parsed.Use))
	}
	mainModuleDir, err := canonicalDirectory(filepath.Join(filepath.Dir(absolutePath), filepath.FromSlash(parsed.Use[0].Path)))
	if err != nil {
		return Settings{}, fmt.Errorf("resolve active go.work module: %w", err)
	}
	goModPath = filepath.Join(mainModuleDir, "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		return Settings{}, fmt.Errorf("snapshot main module file %q: %w", goModPath, err)
	}
	return Settings{
		goWorkPath:    absolutePath,
		goWork:        append([]byte(nil), contents...),
		goModPath:     goModPath,
		goMod:         append([]byte(nil), goMod...),
		mainModuleDir: mainModuleDir,
	}, nil
}

func discoverModuleMode(goModPath string) (Settings, error) {
	if goModPath == "" || goModPath == os.DevNull {
		return Settings{}, fmt.Errorf("no active go.mod; gomcdc requires a main Go module")
	}
	return SnapshotModule(goModPath)
}

// SnapshotModule freezes a standalone main module with GOWORK=off. It is used
// by lower-level workspace callers that already own module discovery.
func SnapshotModule(goModPath string) (Settings, error) {
	absolutePath, err := filepath.Abs(goModPath)
	if err != nil {
		return Settings{}, fmt.Errorf("resolve active go.mod %q: %w", goModPath, err)
	}
	mainModuleDir, err := canonicalDirectory(filepath.Dir(absolutePath))
	if err != nil {
		return Settings{}, fmt.Errorf("resolve active module directory %q: %w", filepath.Dir(absolutePath), err)
	}
	absolutePath = filepath.Join(mainModuleDir, filepath.Base(absolutePath))
	contents, err := os.ReadFile(absolutePath)
	if err != nil {
		return Settings{}, fmt.Errorf("snapshot active go.mod %q: %w", absolutePath, err)
	}
	return Settings{
		goModPath:     absolutePath,
		goMod:         append([]byte(nil), contents...),
		mainModuleDir: mainModuleDir,
	}, nil
}

// Active reports whether the request uses a snapshotted go.work.
func (settings Settings) Active() bool { return settings.goWorkPath != "" }

// Valid reports whether the settings own a frozen go.mod.
func (settings Settings) Valid() bool { return settings.goModPath != "" }

// GoWorkPath returns the source go.work path for diagnostics and tests. Test
// execution must use RelocatedGoWork instead of this source path.
func (settings Settings) GoWorkPath() string { return settings.goWorkPath }

// AssertMainModule proves that package loading selected the module owned by the
// frozen settings.
func (settings Settings) AssertMainModule(moduleDir string) error {
	canonical, err := canonicalDirectory(moduleDir)
	if err != nil {
		return fmt.Errorf("resolve loaded main module: %w", err)
	}
	if canonical != settings.mainModuleDir {
		return fmt.Errorf("frozen module settings own main module %q, not loaded main module %q", settings.mainModuleDir, canonical)
	}
	return nil
}

// AssertSourceUnchanged closes the analysis/configuration race: package
// loading is accepted only if the go.mod and optional go.work still equal the
// bytes captured immediately before loading.
func (settings Settings) AssertSourceUnchanged() error {
	files := []struct {
		path string
		want []byte
	}{{path: settings.goModPath, want: settings.goMod}}
	if settings.Active() {
		files = append(files, struct {
			path string
			want []byte
		}{path: settings.goWorkPath, want: settings.goWork})
	}
	for _, file := range files {
		contents, err := os.ReadFile(file.path)
		if err != nil {
			return fmt.Errorf("verify frozen module settings %q: %w", file.path, err)
		}
		if !bytes.Equal(contents, file.want) {
			return fmt.Errorf("module settings %q changed during package loading", file.path)
		}
	}
	return nil
}

// RelocatedGoMod derives the copied module file exclusively from the frozen
// source bytes and relocates every local replacement away from the original
// module tree.
func (settings Settings) RelocatedGoMod(ctx context.Context, copiedModuleDir string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parsed, err := modfile.Parse(settings.goModPath, settings.goMod, nil)
	if err != nil {
		return nil, fmt.Errorf("parse snapshotted go.mod %q: %w", settings.goModPath, err)
	}
	for _, replacement := range append([]*modfile.Replace(nil), parsed.Replace...) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if replacement.New.Version != "" {
			continue
		}
		path, pathErr := relocateLocalPath(settings.mainModuleDir, settings.mainModuleDir, copiedModuleDir, replacement.New.Path)
		if pathErr != nil {
			return nil, fmt.Errorf("resolve module replacement %q: %w", replacement.New.Path, pathErr)
		}
		if err := parsed.AddReplace(replacement.Old.Path, replacement.Old.Version, path, ""); err != nil {
			return nil, fmt.Errorf("rewrite module replacement for %q: %w", replacement.Old.Path, err)
		}
	}
	formatted, err := parsed.Format()
	if err != nil {
		return nil, fmt.Errorf("format copied go.mod: %w", err)
	}
	return formatted, nil
}

// RelocatedGoWork derives a go.work for a copied module from the immutable
// source snapshot. It preserves Go, toolchain, godebug, and replace settings,
// relocates the sole use directive to copiedModuleDir, and resolves local
// replacements without leaving references back into the original module.
func (settings Settings) RelocatedGoWork(ctx context.Context, sourceModuleDir, copiedWorkspaceDir, copiedModuleDir string) ([]byte, error) {
	if !settings.Active() {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := settings.AssertMainModule(sourceModuleDir); err != nil {
		return nil, err
	}
	if !containsPath(copiedWorkspaceDir, copiedModuleDir) {
		return nil, fmt.Errorf("copied module %q is outside copied workspace %q", copiedModuleDir, copiedWorkspaceDir)
	}
	usePath, err := filepath.Rel(copiedWorkspaceDir, copiedModuleDir)
	if err != nil {
		return nil, fmt.Errorf("locate copied module in workspace: %w", err)
	}
	if usePath != "." && !strings.HasPrefix(usePath, "."+string(filepath.Separator)) {
		usePath = "." + string(filepath.Separator) + usePath
	}
	parsed, err := modfile.ParseWork(settings.goWorkPath, settings.goWork, nil)
	if err != nil {
		return nil, fmt.Errorf("parse snapshotted go.work %q: %w", settings.goWorkPath, err)
	}
	modulePath := parsed.Use[0].ModulePath
	if err := parsed.DropUse(parsed.Use[0].Path); err != nil {
		return nil, fmt.Errorf("remove source go.work module path: %w", err)
	}
	parsed.Cleanup()
	if err := parsed.AddUse(filepath.ToSlash(usePath), modulePath); err != nil {
		return nil, fmt.Errorf("add copied go.work module path: %w", err)
	}
	for _, replacement := range append([]*modfile.Replace(nil), parsed.Replace...) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if replacement.New.Version != "" {
			continue
		}
		path, pathErr := relocateLocalPath(filepath.Dir(settings.goWorkPath), settings.mainModuleDir, copiedModuleDir, replacement.New.Path)
		if pathErr != nil {
			return nil, fmt.Errorf("resolve workspace replacement %q: %w", replacement.New.Path, pathErr)
		}
		if err := parsed.AddReplace(replacement.Old.Path, replacement.Old.Version, path, ""); err != nil {
			return nil, fmt.Errorf("rewrite workspace replacement for %q: %w", replacement.Old.Path, err)
		}
	}
	parsed.Cleanup()
	return modfile.Format(parsed.Syntax), nil
}

func relocateLocalPath(baseDir, sourceModuleDir, copiedModuleDir, path string) (string, error) {
	resolved := filepath.FromSlash(path)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(baseDir, resolved)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if canonical, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
		absolute = canonical
	}
	if !containsPath(sourceModuleDir, absolute) {
		return absolute, nil
	}
	relative, err := filepath.Rel(sourceModuleDir, absolute)
	if err != nil {
		return "", err
	}
	return filepath.Join(copiedModuleDir, relative), nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return filepath.Clean(canonical), nil
}

func containsPath(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
