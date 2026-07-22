// Package loader resolves the package patterns that are eligible for
// instrumentation. Source/type diagnostics remain owned by go test so that a
// broken package does not prevent other matched packages from producing a
// partial coverage report.
package loader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shrydev2020/gomcdc/internal/goflags"
	"github.com/shrydev2020/gomcdc/internal/modulecontext"
	"golang.org/x/tools/go/packages"
)

type Options struct {
	Dir          string
	Patterns     []string
	BuildFlags   []string
	IncludeTests bool
	// GOFLAGS, when non-nil, overrides the inherited value for package loading.
	// The CLI uses this to remove measurement-owned flags while preserving tags.
	GOFLAGS *string
}

type File struct {
	Path        string
	PackagePath string
	PackageName string
}

// WorkingDirectoryBase identifies which frozen source root owns
// RelativeWorkDir. Module is the ordinary case; Workspace is used when a
// single-module go.work command starts above the module directory.
type WorkingDirectoryBase uint8

const (
	WorkingDirectoryModule WorkingDirectoryBase = iota
	WorkingDirectoryWorkspace
)

type Result struct {
	ModulePath       string
	ModuleRoot       string
	WorkingDir       string
	RelativeWorkDir  string
	WorkingDirBase   WorkingDirectoryBase
	ModuleSettings   modulecontext.Settings
	Files            []File
	PackageImportSet []string
	// CoverPackageImportSet contains production packages whose initial load had
	// no build/type diagnostics. Excluding broken packages prevents -coverpkg
	// from making otherwise healthy package tests fail while still aligning C0
	// with cross-package AST observations for every buildable target.
	CoverPackageImportSet []string
}

// Load resolves active source files for package patterns in one main module.
// Multi-module workspaces are detected and rejected instead of silently
// reporting partial coverage.
func Load(ctx context.Context, opts Options) (Result, error) {
	if len(opts.Patterns) == 0 {
		opts.Patterns = []string{"."}
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve working directory: %w", err)
	}
	goFlags := os.Getenv("GOFLAGS")
	if opts.GOFLAGS != nil {
		goFlags = *opts.GOFLAGS
	}
	moduleSettings, err := modulecontext.Discover(ctx, modulecontext.DiscoverOptions{
		Dir:        dir,
		BuildFlags: opts.BuildFlags,
		GOFLAGS:    goFlags,
	})
	if err != nil {
		return Result{}, err
	}
	packageGOFLAGS := goFlags
	packageBuildFlags := append([]string(nil), opts.BuildFlags...)
	if moduleSettings.HasAlternateModFile() {
		// x/tools/go/packages performs a preliminary `go env` invocation.
		// cmd/go rejects -modfile for that verb, so normalize the effective
		// selection into the list/build flags and keep it out of that env call.
		packageGOFLAGS, err = goflags.Without(goFlags, map[string]bool{"modfile": true})
		if err != nil {
			return Result{}, fmt.Errorf("parse GOFLAGS for package loading: %w", err)
		}
		packageBuildFlags = withoutBuildFlag(packageBuildFlags, "modfile")
		packageBuildFlags = append(packageBuildFlags, "-modfile="+moduleSettings.AlternateModFilePath())
	}

	mode := packages.NeedName |
		packages.NeedFiles |
		packages.NeedCompiledGoFiles |
		packages.NeedModule |
		packages.NeedSyntax |
		packages.NeedTypes |
		packages.NeedTypesInfo
	cfg := &packages.Config{
		Context:    ctx,
		Mode:       mode,
		Dir:        dir,
		BuildFlags: packageBuildFlags,
		Tests:      false,
	}
	if opts.GOFLAGS != nil || moduleSettings.HasAlternateModFile() {
		cfg.Env = overrideEnvironment("GOFLAGS", packageGOFLAGS)
	}
	pkgs, err := packages.Load(cfg, opts.Patterns...)
	if err != nil {
		return Result{}, fmt.Errorf("load packages: %w", err)
	}
	if err := moduleSettings.AssertSourceUnchanged(); err != nil {
		return Result{}, err
	}
	if len(pkgs) == 0 {
		return Result{}, errors.New("load packages: no packages matched")
	}
	modulePath, moduleRoot, err := commonMainModule(pkgs)
	if err != nil {
		return Result{}, err
	}
	if err := moduleSettings.AssertMainModule(moduleRoot); err != nil {
		return Result{}, err
	}
	workingDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve working directory links: %w", err)
	}
	files := make(map[string]File)
	imports := make(map[string]struct{})
	coverImports := make(map[string]struct{})
	addPackageFiles(files, imports, pkgs, moduleRoot, modulePath, false, true)
	for _, pkg := range pkgs {
		if pkg.Module != nil && pkg.Module.Main && pkg.Module.Path == modulePath && len(pkg.Errors) == 0 {
			coverImports[pkg.PkgPath] = struct{}{}
		}
	}

	if opts.IncludeTests {
		testCfg := *cfg
		testCfg.Tests = true
		// Test packages are metadata-only so their compile/type errors remain
		// go test failures and can still yield a partial runtime report.
		testCfg.Mode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedModule
		testPkgs, loadErr := packages.Load(&testCfg, opts.Patterns...)
		if loadErr != nil && len(testPkgs) == 0 {
			return Result{}, fmt.Errorf("load test package metadata: %w", loadErr)
		}
		if err := moduleSettings.AssertSourceUnchanged(); err != nil {
			return Result{}, err
		}
		// Do not promote test-package diagnostics to an instrumentation error.
		// The actual go test command owns compile/test failures and can still
		// leave useful events from other packages behind.
		addPackageFiles(files, imports, testPkgs, moduleRoot, modulePath, true, false)
	}

	if len(files) == 0 {
		return Result{}, errors.New("load packages: matched packages contain no instrumentable Go files")
	}
	workBase := WorkingDirectoryModule
	relWork, err := relativeWithin(moduleRoot, workingDir)
	if err != nil && moduleSettings.Active() {
		workspaceRoot := filepath.Dir(moduleSettings.GoWorkPath())
		if workingDir == workspaceRoot {
			relWork, err = ".", nil
			workBase = WorkingDirectoryWorkspace
		}
	}
	if err != nil {
		return Result{}, fmt.Errorf("working directory %q is outside the active module and is not the single-module workspace root", workingDir)
	}

	result := Result{
		ModulePath:      modulePath,
		ModuleRoot:      moduleRoot,
		WorkingDir:      workingDir,
		RelativeWorkDir: relWork,
		WorkingDirBase:  workBase,
		ModuleSettings:  moduleSettings,
	}
	for _, file := range files {
		result.Files = append(result.Files, file)
	}
	for importPath := range imports {
		result.PackageImportSet = append(result.PackageImportSet, importPath)
	}
	for importPath := range coverImports {
		result.CoverPackageImportSet = append(result.CoverPackageImportSet, importPath)
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	sort.Strings(result.PackageImportSet)
	sort.Strings(result.CoverPackageImportSet)
	return result, nil
}

func withoutBuildFlag(flags []string, name string) []string {
	result := make([]string, 0, len(flags))
	for index := 0; index < len(flags); index++ {
		flag := flags[index]
		if goflags.Name(flag) == name {
			if !strings.Contains(flag, "=") && index+1 < len(flags) {
				index++
			}
			continue
		}
		result = append(result, flag)
	}
	return result
}

func relativeWithin(root, candidate string) (string, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside %q", candidate, root)
	}
	return relative, nil
}

func overrideEnvironment(key, value string) []string {
	prefix := key + "="
	environment := make([]string, 0)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			environment = append(environment, entry)
		}
	}
	return append(environment, prefix+value)
}

func commonMainModule(pkgs []*packages.Package) (modulePath, moduleRoot string, err error) {
	for _, pkg := range pkgs {
		if pkg.Module == nil || !pkg.Module.Main {
			return "", "", fmt.Errorf("package %q is not in the main module", pkg.PkgPath)
		}
		root, absErr := filepath.Abs(pkg.Module.Dir)
		if absErr != nil {
			return "", "", fmt.Errorf("resolve module root for %q: %w", pkg.PkgPath, absErr)
		}
		root, absErr = filepath.EvalSymlinks(root)
		if absErr != nil {
			return "", "", fmt.Errorf("resolve module root links for %q: %w", pkg.PkgPath, absErr)
		}
		if modulePath == "" {
			modulePath, moduleRoot = pkg.Module.Path, root
			continue
		}
		if pkg.Module.Path != modulePath || root != moduleRoot {
			return "", "", errors.New("multiple main modules matched; run gomcdc once per main module")
		}
	}
	if modulePath == "" || moduleRoot == "" {
		return "", "", errors.New("no main Go module found for package patterns")
	}
	return modulePath, moduleRoot, nil
}

func addPackageFiles(
	dst map[string]File,
	imports map[string]struct{},
	pkgs []*packages.Package,
	moduleRoot string,
	modulePath string,
	testsOnly bool,
	collectImports bool,
) {
	for _, pkg := range pkgs {
		if pkg.Module == nil || !pkg.Module.Main || pkg.Module.Path != modulePath {
			continue
		}
		for _, name := range pkg.GoFiles {
			isTest := strings.HasSuffix(name, "_test.go")
			if testsOnly != isTest {
				continue
			}
			abs, err := filepath.Abs(name)
			if err != nil {
				continue
			}
			abs, err = filepath.EvalSymlinks(abs)
			if err != nil || !within(moduleRoot, abs) {
				continue
			}
			if _, exists := dst[abs]; exists {
				continue
			}
			dst[abs] = File{Path: abs, PackagePath: pkg.PkgPath, PackageName: pkg.Name}
			if collectImports {
				imports[pkg.PkgPath] = struct{}{}
			}
		}
	}
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
