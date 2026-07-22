// Package modulecontext captures caller-owned Go module configuration for
// relocation into a disposable request workspace.
package modulecontext

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/goflags"
	"github.com/shrydev2020/gomcdc/v2/internal/processgroup"
	"golang.org/x/mod/modfile"
)

// SourceConfiguration is the immutable module configuration captured from the
// caller's source tree. It seeds a disposable request workspace; Go commands
// must use that workspace rather than these source paths so legitimate module
// resolution updates never mutate the caller's files.
type SourceConfiguration struct {
	goWorkPath       string
	goWork           []byte
	goWorkSum        []byte
	goWorkSumSet     bool
	goModPath        string
	goMod            []byte
	alternateModPath string
	alternateMod     []byte
	alternateSumPath string
	alternateSum     []byte
	alternateSumSet  bool
	mainModuleDir    string
}

// DiscoverOptions contains the exact module-selection inputs passed to package
// loading. Command-line BuildFlags follow GOFLAGS and therefore take
// precedence when both select -modfile.
type DiscoverOptions struct {
	Dir        string
	BuildFlags []string
	GOFLAGS    string
}

// Discover captures the active source configuration, rejecting a workspace
// that does not have exactly one main module. The result must seed a disposable
// workspace before package loading begins.
func Discover(ctx context.Context, options DiscoverOptions) (SourceConfiguration, error) {
	alternateModPath, err := selectedAlternateModFile(options.Dir, options.GOFLAGS, options.BuildFlags)
	if err != nil {
		return SourceConfiguration{}, err
	}
	discoveryGOFLAGS, err := goflags.Without(options.GOFLAGS, map[string]bool{"modfile": true})
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("parse GOFLAGS for primary module discovery: %w", err)
	}
	command := exec.CommandContext(ctx, "go", "env", "GOWORK", "GOMOD")
	processgroup.ConfigureCancellation(command)
	command.Dir = options.Dir
	// GOMOD must identify the required primary go.mod even when package loading
	// selects an alternate modfile. The alternate file is frozen separately.
	command.Env = overrideEnvironment(os.Environ(), "GOFLAGS", discoveryGOFLAGS)
	output, err := command.Output()
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("inspect Go workspace: %w", err)
	}
	values := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	if len(values) != 2 {
		return SourceConfiguration{}, fmt.Errorf("inspect Go module settings: go env returned %d values", len(values))
	}
	workspacePath := strings.TrimSuffix(values[0], "\r")
	goModPath := strings.TrimSuffix(values[1], "\r")
	if workspacePath == "" || workspacePath == "off" {
		settings, discoverErr := discoverModuleMode(goModPath)
		if discoverErr != nil {
			return SourceConfiguration{}, discoverErr
		}
		return settings.withAlternateModFile(alternateModPath)
	}
	if alternateModPath != "" {
		return SourceConfiguration{}, fmt.Errorf("alternate modfile %q cannot be used with active go.work", alternateModPath)
	}
	absolutePath, err := filepath.Abs(workspacePath)
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("resolve active go.work %q: %w", workspacePath, err)
	}
	absolutePath, err = filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("resolve active go.work links %q: %w", workspacePath, err)
	}
	contents, err := os.ReadFile(absolutePath)
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("inspect active go.work %q: %w", absolutePath, err)
	}
	parsed, err := modfile.ParseWork(absolutePath, contents, nil)
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("inspect active go.work %q: %w", absolutePath, err)
	}
	if len(parsed.Use) != 1 {
		return SourceConfiguration{}, fmt.Errorf("active go.work %q has %d main modules; exactly one is required", absolutePath, len(parsed.Use))
	}
	goWorkSum, goWorkSumErr := os.ReadFile(absolutePath + ".sum")
	goWorkSumSet := goWorkSumErr == nil
	if goWorkSumErr != nil && !os.IsNotExist(goWorkSumErr) {
		return SourceConfiguration{}, fmt.Errorf("snapshot active workspace sum %q: %w", absolutePath+".sum", goWorkSumErr)
	}
	mainModuleDir, err := canonicalDirectory(filepath.Join(filepath.Dir(absolutePath), filepath.FromSlash(parsed.Use[0].Path)))
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("resolve active go.work module: %w", err)
	}
	goModPath = filepath.Join(mainModuleDir, "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("snapshot main module file %q: %w", goModPath, err)
	}
	return SourceConfiguration{
		goWorkPath:    absolutePath,
		goWork:        append([]byte(nil), contents...),
		goWorkSum:     append([]byte(nil), goWorkSum...),
		goWorkSumSet:  goWorkSumSet,
		goModPath:     goModPath,
		goMod:         append([]byte(nil), goMod...),
		mainModuleDir: mainModuleDir,
	}, nil
}

func selectedAlternateModFile(dir, goFlags string, buildFlags []string) (string, error) {
	goFlagWords, err := goflags.Split(goFlags)
	if err != nil {
		return "", fmt.Errorf("parse GOFLAGS for module settings: %w", err)
	}
	selected, err := modFileFlagValue(goFlagWords, false)
	if err != nil {
		return "", fmt.Errorf("parse GOFLAGS for module settings: %w", err)
	}
	if explicit, explicitErr := modFileFlagValue(buildFlags, true); explicitErr != nil {
		return "", fmt.Errorf("parse build flags for module settings: %w", explicitErr)
	} else if explicit != "" {
		selected = explicit
	}
	if selected == "" {
		return "", nil
	}
	if !strings.HasSuffix(selected, ".mod") {
		return "", fmt.Errorf("alternate modfile %q does not have .mod extension", selected)
	}
	if !filepath.IsAbs(selected) {
		selected = filepath.Join(dir, selected)
	}
	absolute, err := filepath.Abs(selected)
	if err != nil {
		return "", fmt.Errorf("resolve alternate modfile %q: %w", selected, err)
	}
	return filepath.Clean(absolute), nil
}

func modFileFlagValue(words []string, separateValue bool) (string, error) {
	selected := ""
	for index := 0; index < len(words); index++ {
		word := words[index]
		if goflags.Name(word) != "modfile" {
			continue
		}
		if separator := strings.IndexByte(word, '='); separator >= 0 {
			selected = word[separator+1:]
		} else {
			if !separateValue {
				return "", fmt.Errorf("GOFLAGS modfile must use -modfile=value")
			}
			if index+1 >= len(words) {
				return "", fmt.Errorf("flag %s requires a value", word)
			}
			index++
			selected = words[index]
		}
		if selected == "" {
			return "", fmt.Errorf("modfile path is empty")
		}
	}
	return selected, nil
}

func (settings SourceConfiguration) withAlternateModFile(path string) (SourceConfiguration, error) {
	if path == "" {
		return settings, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("snapshot alternate modfile %q: %w", path, err)
	}
	sumPath := strings.TrimSuffix(path, ".mod") + ".sum"
	sum, sumErr := os.ReadFile(sumPath)
	sumSet := sumErr == nil
	if sumErr != nil && !os.IsNotExist(sumErr) {
		return SourceConfiguration{}, fmt.Errorf("snapshot alternate sum file %q: %w", sumPath, sumErr)
	}
	settings.alternateModPath = path
	settings.alternateMod = append([]byte(nil), contents...)
	settings.alternateSumPath = sumPath
	settings.alternateSum = append([]byte(nil), sum...)
	settings.alternateSumSet = sumSet
	return settings, nil
}

func discoverModuleMode(goModPath string) (SourceConfiguration, error) {
	if goModPath == "" || goModPath == os.DevNull {
		return SourceConfiguration{}, fmt.Errorf("no active go.mod; gomcdc requires a main Go module")
	}
	return SnapshotModule(goModPath)
}

// SnapshotModule captures a standalone main module with GOWORK=off. It is
// used by lower-level workspace callers that already own module discovery.
func SnapshotModule(goModPath string) (SourceConfiguration, error) {
	absolutePath, err := filepath.Abs(goModPath)
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("resolve active go.mod %q: %w", goModPath, err)
	}
	mainModuleDir, err := canonicalDirectory(filepath.Dir(absolutePath))
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("resolve active module directory %q: %w", filepath.Dir(absolutePath), err)
	}
	absolutePath = filepath.Join(mainModuleDir, filepath.Base(absolutePath))
	contents, err := os.ReadFile(absolutePath)
	if err != nil {
		return SourceConfiguration{}, fmt.Errorf("snapshot active go.mod %q: %w", absolutePath, err)
	}
	return SourceConfiguration{
		goModPath:     absolutePath,
		goMod:         append([]byte(nil), contents...),
		mainModuleDir: mainModuleDir,
	}, nil
}

// Active reports whether the source configuration uses go.work.
func (settings SourceConfiguration) Active() bool { return settings.goWorkPath != "" }

// Valid reports whether the source configuration contains a main go.mod.
func (settings SourceConfiguration) Valid() bool { return settings.goModPath != "" }

// GoWorkPath returns the source go.work path for diagnostics and workspace
// construction. Go commands must use the relocated request-owned copy.
func (settings SourceConfiguration) GoWorkPath() string { return settings.goWorkPath }

// MainModuleDir returns the source main-module directory owned by the
// configuration. Go commands must use the request workspace's ModuleDir.
func (settings SourceConfiguration) MainModuleDir() string { return settings.mainModuleDir }

// HasAlternateModFile reports whether -modfile selected an alternate source
// module file for this request.
func (settings SourceConfiguration) HasAlternateModFile() bool {
	return settings.alternateModPath != ""
}

// AlternateModFilePath returns the selected source alternate modfile path for
// diagnostics. Package loading and test execution must use the relocated copy.
func (settings SourceConfiguration) AlternateModFilePath() string { return settings.alternateModPath }

// AssertMainModule proves that a directory is the source module owned by this
// configuration.
func (settings SourceConfiguration) AssertMainModule(moduleDir string) error {
	canonical, err := canonicalDirectory(moduleDir)
	if err != nil {
		return fmt.Errorf("resolve loaded main module: %w", err)
	}
	if canonical != settings.mainModuleDir {
		return fmt.Errorf("source module configuration owns main module %q, not %q", settings.mainModuleDir, canonical)
	}
	return nil
}

// RelocatedGoMod derives the copied module file exclusively from the frozen
// source bytes and relocates every local replacement away from the original
// module tree.
func (settings SourceConfiguration) RelocatedGoMod(ctx context.Context, copiedModuleDir string) ([]byte, error) {
	return settings.relocatedModuleFile(ctx, settings.goModPath, settings.goMod, copiedModuleDir)
}

// RelocatedAlternateMod derives the selected alternate modfile from its frozen
// bytes and returns the matching frozen sum bytes. Local replacements are
// relocated against the copied main module just as they are for go.mod.
func (settings SourceConfiguration) RelocatedAlternateMod(ctx context.Context, copiedModuleDir string) (mod, sum []byte, sumSet bool, err error) {
	if !settings.HasAlternateModFile() {
		return nil, nil, false, nil
	}
	mod, err = settings.relocatedModuleFile(ctx, settings.alternateModPath, settings.alternateMod, copiedModuleDir)
	if err != nil {
		return nil, nil, false, err
	}
	return mod, append([]byte(nil), settings.alternateSum...), settings.alternateSumSet, nil
}

func (settings SourceConfiguration) relocatedModuleFile(ctx context.Context, sourcePath string, source []byte, copiedModuleDir string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parsed, err := modfile.Parse(sourcePath, source, nil)
	if err != nil {
		return nil, fmt.Errorf("parse snapshotted module file %q: %w", sourcePath, err)
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

func overrideEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

// RelocatedGoWork derives a request-owned go.work from the source snapshot. It
// preserves Go, toolchain, godebug, and replace settings, and relocates the
// sole use directive to copiedModuleDir. The module may be outside the copied
// workspace directory when the source go.work itself uses such a layout.
func (settings SourceConfiguration) RelocatedGoWork(ctx context.Context, sourceModuleDir, copiedWorkspaceDir, copiedModuleDir string) ([]byte, error) {
	if !settings.Active() {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := settings.AssertMainModule(sourceModuleDir); err != nil {
		return nil, err
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

// GoWorkSum returns the captured source go.work.sum bytes, when present.
// Legitimate updates are allowed only after these bytes are copied into the
// request workspace.
func (settings SourceConfiguration) GoWorkSum() ([]byte, bool) {
	return append([]byte(nil), settings.goWorkSum...), settings.goWorkSumSet
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
