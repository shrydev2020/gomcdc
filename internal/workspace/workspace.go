// Package workspace creates disposable copies of Go module trees.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shrydev2020/gomcdc/v2/internal/modulecontext"
)

const tempPrefix = "gomcdc-"

// Options controls creation and lifetime of a Workspace.
type Options struct {
	// SourceConfiguration supplies the caller-owned module files used to seed
	// the request workspace. Go commands never receive its source paths.
	SourceConfiguration modulecontext.SourceConfiguration
	// WorkingDir is the caller's invocation directory. The workspace preserves
	// its location relative to the source module and active go.work.
	// An empty value defaults to the source main-module directory.
	WorkingDir string
	// TempParent is the directory under which the workspace is created. An
	// empty value uses os.TempDir. It must not be the source module directory
	// or one of its descendants, otherwise the copy could include itself.
	TempParent string
	// Keep causes Cleanup to retain the workspace for inspection.
	Keep bool
}

// Workspace is an isolated copy of a module and its separate runtime event
// directory. Its paths remain available after cleanup so they can be reported
// to the user.
type Workspace struct {
	SourceModuleDir  string
	SourceWorkingDir string
	RootDir          string
	MappedRootDir    string
	WorkspaceDir     string
	ModuleDir        string
	WorkingDir       string
	EventDir         string
	GoWorkPath       string
	ModFilePath      string

	mu      sync.Mutex
	keep    bool
	removed bool
}

// Create copies a module tree while ctx permits new workspace work. A
// cancellation removes any partially-created workspace before returning.
func Create(ctx context.Context, options Options) (_ *Workspace, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !options.SourceConfiguration.Valid() {
		return nil, errors.New("source module configuration is required")
	}
	sourceDir, err := existingDirectory(options.SourceConfiguration.MainModuleDir(), "source module directory")
	if err != nil {
		return nil, err
	}
	if err := options.SourceConfiguration.AssertMainModule(sourceDir); err != nil {
		return nil, err
	}
	sourceWorkingDir := options.WorkingDir
	if sourceWorkingDir == "" {
		sourceWorkingDir = sourceDir
	}
	sourceWorkingDir, err = existingDirectory(sourceWorkingDir, "source working directory")
	if err != nil {
		return nil, err
	}
	sourceWorkspaceDir := sourceDir
	if options.SourceConfiguration.Active() {
		sourceWorkspaceDir, err = existingDirectory(filepath.Dir(options.SourceConfiguration.GoWorkPath()), "source workspace directory")
		if err != nil {
			return nil, err
		}
	}
	topologyRoot, err := commonDirectory(sourceDir, sourceWorkspaceDir, sourceWorkingDir)
	if err != nil {
		return nil, fmt.Errorf("map source topology: %w", err)
	}

	tempParent := options.TempParent
	if tempParent == "" {
		tempParent = os.TempDir()
	}
	tempParent, err = existingDirectory(tempParent, "temporary workspace parent")
	if err != nil {
		return nil, err
	}
	if containsPath(sourceDir, tempParent) {
		return nil, fmt.Errorf("temporary workspace parent %q must be outside source tree %q", tempParent, sourceDir)
	}

	rootDir, err := os.MkdirTemp(tempParent, tempPrefix)
	if err != nil {
		return nil, fmt.Errorf("create temporary workspace: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			err = errors.Join(err, os.RemoveAll(rootDir))
		}
	}()

	topologyDir := filepath.Join(rootDir, "source")
	moduleDir, err := mappedPath(topologyRoot, topologyDir, sourceDir)
	if err != nil {
		return nil, fmt.Errorf("map source module: %w", err)
	}
	workspaceDir, err := mappedPath(topologyRoot, topologyDir, sourceWorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("map source workspace: %w", err)
	}
	workingDir, err := mappedPath(topologyRoot, topologyDir, sourceWorkingDir)
	if err != nil {
		return nil, fmt.Errorf("map source working directory: %w", err)
	}
	if err := os.MkdirAll(moduleDir, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace module directory: %w", err)
	}
	if err := copyTree(ctx, sourceDir, moduleDir); err != nil {
		return nil, fmt.Errorf("copy module tree: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	goMod, err := options.SourceConfiguration.RelocatedGoMod(ctx, moduleDir)
	if err != nil {
		return nil, fmt.Errorf("relocate copied go.mod: %w", err)
	}
	goModPath := filepath.Join(moduleDir, "go.mod")
	goModInfo, err := os.Stat(goModPath)
	if err != nil {
		return nil, fmt.Errorf("inspect copied go.mod: %w", err)
	}
	if err := os.WriteFile(goModPath, goMod, goModInfo.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("write copied go.mod: %w", err)
	}
	modFilePath := ""
	if options.SourceConfiguration.HasAlternateModFile() {
		configDir := filepath.Join(rootDir, "module-config")
		if err := os.Mkdir(configDir, 0o700); err != nil {
			return nil, fmt.Errorf("create alternate module configuration directory: %w", err)
		}
		modContents, sumContents, sumSet, relocateErr := options.SourceConfiguration.RelocatedAlternateMod(ctx, moduleDir)
		if relocateErr != nil {
			return nil, fmt.Errorf("relocate alternate modfile: %w", relocateErr)
		}
		modFilePath = filepath.Join(configDir, "gomcdc.mod")
		if err := os.WriteFile(modFilePath, modContents, 0o600); err != nil {
			return nil, fmt.Errorf("write alternate modfile: %w", err)
		}
		if sumSet {
			sumPath := strings.TrimSuffix(modFilePath, ".mod") + ".sum"
			if err := os.WriteFile(sumPath, sumContents, 0o600); err != nil {
				return nil, fmt.Errorf("write alternate sum file: %w", err)
			}
		}
	}
	goWorkPath := ""
	if options.SourceConfiguration.Active() {
		if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
			return nil, fmt.Errorf("create copied Go workspace directory: %w", err)
		}
		goWorkPath = filepath.Join(workspaceDir, "go.work")
		contents, relocateErr := options.SourceConfiguration.RelocatedGoWork(ctx, sourceDir, workspaceDir, moduleDir)
		if relocateErr != nil {
			return nil, fmt.Errorf("relocate copied Go workspace: %w", relocateErr)
		}
		if err := os.WriteFile(goWorkPath, contents, 0o600); err != nil {
			return nil, fmt.Errorf("write copied Go workspace: %w", err)
		}
		if sum, present := options.SourceConfiguration.GoWorkSum(); present {
			if err := os.WriteFile(goWorkPath+".sum", sum, 0o600); err != nil {
				return nil, fmt.Errorf("write copied Go workspace sum: %w", err)
			}
		}
	}
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		return nil, fmt.Errorf("create mapped working directory: %w", err)
	}

	eventDir := filepath.Join(rootDir, "events")
	if err := os.Mkdir(eventDir, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace event directory: %w", err)
	}

	removeOnError = false
	return &Workspace{
		SourceModuleDir:  sourceDir,
		SourceWorkingDir: sourceWorkingDir,
		RootDir:          rootDir,
		MappedRootDir:    topologyDir,
		WorkspaceDir:     workspaceDir,
		ModuleDir:        moduleDir,
		WorkingDir:       workingDir,
		EventDir:         eventDir,
		GoWorkPath:       goWorkPath,
		ModFilePath:      modFilePath,
		keep:             options.Keep,
	}, nil
}

func commonDirectory(paths ...string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("at least one source path is required")
	}
	common := filepath.Clean(paths[0])
	for _, path := range paths[1:] {
		path = filepath.Clean(path)
		for !containsPath(common, path) {
			parent := filepath.Dir(common)
			if parent == common {
				return "", fmt.Errorf("paths %q and %q do not share a filesystem root", paths[0], path)
			}
			common = parent
		}
	}
	return common, nil
}

func mappedPath(sourceRoot, copiedRoot, sourcePath string) (string, error) {
	relative, err := filepath.Rel(sourceRoot, sourcePath)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source path %q is outside topology root %q", sourcePath, sourceRoot)
	}
	if relative == "." {
		return copiedRoot, nil
	}
	return filepath.Join(copiedRoot, relative), nil
}

// Keep marks the workspace for retention. It is safe to call before a
// deferred Cleanup, for example after an instrumentation failure.
func (w *Workspace) Keep() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keep = true
}

// IsKept reports whether Cleanup will retain this workspace.
func (w *Workspace) IsKept() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.keep
}

// Cleanup removes the workspace unless it has been marked for retention.
// Cleanup is idempotent. Use Remove to delete a retained workspace explicitly.
func (w *Workspace) Cleanup() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.keep || w.removed {
		return nil
	}
	return w.removeLocked()
}

// Remove removes the workspace even when it is marked for retention. Remove
// is idempotent.
func (w *Workspace) Remove() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.removed {
		return nil
	}
	return w.removeLocked()
}

func (w *Workspace) removeLocked() error {
	if err := os.RemoveAll(w.RootDir); err != nil {
		return fmt.Errorf("remove workspace %q: %w", w.RootDir, err)
	}
	w.removed = true
	return nil
}

func existingDirectory(path, description string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s is required", description)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s %q: %w", description, path, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve %s %q: %w", description, path, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect %s %q: %w", description, path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s %q is not a directory", description, path)
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

type directoryMode struct {
	path string
	mode fs.FileMode
}

func copyTree(ctx context.Context, sourceDir, destinationDir string) error {
	var directories []directoryMode
	err := filepath.WalkDir(sourceDir, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceDir, sourcePath)
		if err != nil {
			return fmt.Errorf("resolve relative path for %q: %w", sourcePath, err)
		}
		if relative != "." && entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := os.Lstat(sourcePath)
		if err != nil {
			return fmt.Errorf("inspect %q: %w", sourcePath, err)
		}
		destinationPath := destinationDir
		if relative != "." {
			destinationPath = filepath.Join(destinationDir, relative)
		}

		switch {
		case info.IsDir():
			if relative != "." {
				if err := os.Mkdir(destinationPath, 0o700); err != nil {
					return fmt.Errorf("create directory %q: %w", destinationPath, err)
				}
			}
			directories = append(directories, directoryMode{path: destinationPath, mode: info.Mode()})
			return nil
		case info.Mode().IsRegular():
			if err := copyRegularFile(ctx, sourcePath, destinationPath, info.Mode()); err != nil {
				return err
			}
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(sourcePath)
			if err != nil {
				return fmt.Errorf("read symbolic link %q: %w", sourcePath, err)
			}
			target, err = copiedSymlinkTarget(sourceDir, destinationDir, sourcePath, destinationPath, target)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, destinationPath); err != nil {
				return fmt.Errorf("create symbolic link %q: %w", destinationPath, err)
			}
			return nil
		default:
			return fmt.Errorf("unsupported file type %q (%s)", sourcePath, info.Mode().Type())
		}
	})
	if err != nil {
		return err
	}

	// Restore directory permissions after copying so read-only source
	// directories do not prevent their children from being created.
	for index := len(directories) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Chmod(directories[index].path, directories[index].mode.Perm()); err != nil {
			return fmt.Errorf("preserve directory mode for %q: %w", directories[index].path, err)
		}
	}
	return nil
}

func copiedSymlinkTarget(sourceDir, destinationDir, sourcePath, destinationPath, target string) (string, error) {
	resolvedTarget := target
	if !filepath.IsAbs(resolvedTarget) {
		resolvedTarget = filepath.Join(filepath.Dir(sourcePath), resolvedTarget)
	}
	resolvedTarget, err := filepath.EvalSymlinks(resolvedTarget)
	if err != nil {
		return "", fmt.Errorf("resolve symbolic link %q: %w", sourcePath, err)
	}
	if !containsPath(sourceDir, resolvedTarget) {
		return "", fmt.Errorf("symbolic link %q resolves outside source tree %q", sourcePath, sourceDir)
	}
	relativeToSource, err := filepath.Rel(sourceDir, resolvedTarget)
	if err != nil {
		return "", fmt.Errorf("locate symbolic link target %q in source tree: %w", sourcePath, err)
	}
	copiedTarget := filepath.Join(destinationDir, relativeToSource)
	relativeToLink, err := filepath.Rel(filepath.Dir(destinationPath), copiedTarget)
	if err != nil {
		return "", fmt.Errorf("relocate symbolic link %q into workspace: %w", sourcePath, err)
	}
	return relativeToLink, nil
}

func copyRegularFile(ctx context.Context, sourcePath, destinationPath string, mode fs.FileMode) (err error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source file %q: %w", sourcePath, err)
	}
	defer func() {
		err = errors.Join(err, source.Close())
	}()

	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create destination file %q: %w", destinationPath, err)
	}
	defer func() {
		err = errors.Join(err, destination.Close())
	}()

	if _, err := io.Copy(destination, contextReader{ctx: ctx, reader: source}); err != nil {
		return fmt.Errorf("copy %q to %q: %w", sourcePath, destinationPath, err)
	}
	if err := destination.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("preserve file mode for %q: %w", destinationPath, err)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(destination []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(destination)
}
