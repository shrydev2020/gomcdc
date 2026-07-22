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
	// GOWORK, when non-nil, pins package loading to the request-owned workspace.
	GOWORK *string
	// ExpectedModuleRoot, when non-empty, rejects a package result outside the
	// request-owned main module prepared by the caller.
	ExpectedModuleRoot string
}

type File struct {
	Path        string
	PackagePath string
	PackageName string
}

type Result struct {
	ModulePath       string
	ModuleRoot       string
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
		BuildFlags: append([]string(nil), opts.BuildFlags...),
		Tests:      false,
	}
	if opts.GOFLAGS != nil || opts.GOWORK != nil {
		overrides := make(map[string]string, 2)
		if opts.GOFLAGS != nil {
			overrides["GOFLAGS"] = *opts.GOFLAGS
		}
		if opts.GOWORK != nil {
			overrides["GOWORK"] = *opts.GOWORK
		}
		cfg.Env = overrideEnvironment(overrides)
	}
	pkgs, err := packages.Load(cfg, opts.Patterns...)
	if err != nil {
		return Result{}, fmt.Errorf("load packages: %w", err)
	}
	if len(pkgs) == 0 {
		return Result{}, errors.New("load packages: no packages matched")
	}
	modulePath, moduleRoot, err := commonMainModule(pkgs)
	if err != nil {
		return Result{}, err
	}
	if opts.ExpectedModuleRoot != "" {
		expected, expectedErr := canonicalDirectory(opts.ExpectedModuleRoot)
		if expectedErr != nil {
			return Result{}, fmt.Errorf("resolve expected main module: %w", expectedErr)
		}
		if moduleRoot != expected {
			return Result{}, fmt.Errorf("loaded main module %q, want request workspace module %q", moduleRoot, expected)
		}
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
		// Do not promote test-package diagnostics to an instrumentation error.
		// The actual go test command owns compile/test failures and can still
		// leave useful events from other packages behind.
		addPackageFiles(files, imports, testPkgs, moduleRoot, modulePath, true, false)
	}

	if len(files) == 0 {
		return Result{}, errors.New("load packages: matched packages contain no instrumentable Go files")
	}
	result := Result{
		ModulePath: modulePath,
		ModuleRoot: moduleRoot,
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

func overrideEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; !replaced {
			environment = append(environment, entry)
		}
	}
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
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
