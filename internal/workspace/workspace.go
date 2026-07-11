// Package workspace creates disposable copies of Go module trees.
package workspace

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
)

const tempPrefix = "gomcdc-"

// Options controls creation and lifetime of a Workspace.
type Options struct {
	// SourceDir is the module directory to copy. It must already exist.
	SourceDir string
	// TempParent is the directory under which the workspace is created. An
	// empty value uses os.TempDir. It must not be SourceDir or one of its
	// descendants, otherwise the copy could recursively include itself.
	TempParent string
	// Keep causes Cleanup to retain the workspace for inspection.
	Keep bool
}

// Workspace is an isolated copy of a module and its separate runtime event
// directory. Its paths remain available after cleanup so they can be reported
// to the user.
type Workspace struct {
	SourceDir string
	RootDir   string
	ModuleDir string
	EventDir  string

	mu      sync.Mutex
	keep    bool
	removed bool
}

// Create copies a module tree into a newly-created temporary workspace.
// Git administrative entries are omitted, while all regular files (including
// non-Go assets) and symbolic links are copied.
func Create(options Options) (_ *Workspace, err error) {
	sourceDir, err := existingDirectory(options.SourceDir, "source directory")
	if err != nil {
		return nil, err
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

	moduleDir := filepath.Join(rootDir, "module")
	if err := os.Mkdir(moduleDir, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace module directory: %w", err)
	}
	if err := copyTree(sourceDir, moduleDir); err != nil {
		return nil, fmt.Errorf("copy module tree: %w", err)
	}
	if err := rewriteRelativeReplacements(sourceDir, moduleDir); err != nil {
		return nil, fmt.Errorf("rewrite copied module replacements: %w", err)
	}

	eventDir := filepath.Join(rootDir, "events")
	if err := os.Mkdir(eventDir, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace event directory: %w", err)
	}

	removeOnError = false
	return &Workspace{
		SourceDir: sourceDir,
		RootDir:   rootDir,
		ModuleDir: moduleDir,
		EventDir:  eventDir,
		keep:      options.Keep,
	}, nil
}

// rewriteRelativeReplacements keeps local development dependencies resolvable
// after the main module moves to a temporary directory. Only the copied
// go.mod is changed; replacement modules remain read-only inputs at their
// original absolute paths.
func rewriteRelativeReplacements(sourceDir, moduleDir string) error {
	sourcePath := filepath.Join(sourceDir, "go.mod")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read %q: %w", sourcePath, err)
	}
	parsed, err := modfile.Parse(sourcePath, contents, nil)
	if err != nil {
		return fmt.Errorf("parse %q: %w", sourcePath, err)
	}
	changed := false
	for _, replacement := range parsed.Replace {
		if replacement.New.Version != "" || filepath.IsAbs(replacement.New.Path) {
			continue
		}
		absolute, err := filepath.Abs(filepath.Join(sourceDir, filepath.FromSlash(replacement.New.Path)))
		if err != nil {
			return fmt.Errorf("resolve replacement %q: %w", replacement.New.Path, err)
		}
		if err := parsed.AddReplace(
			replacement.Old.Path,
			replacement.Old.Version,
			absolute,
			"",
		); err != nil {
			return fmt.Errorf("rewrite replacement for %q: %w", replacement.Old.Path, err)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	formatted, err := parsed.Format()
	if err != nil {
		return fmt.Errorf("format copied go.mod: %w", err)
	}
	destination := filepath.Join(moduleDir, "go.mod")
	info, err := os.Stat(destination)
	if err != nil {
		return fmt.Errorf("stat copied go.mod: %w", err)
	}
	if err := os.WriteFile(destination, formatted, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write copied go.mod: %w", err)
	}
	return nil
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

func copyTree(sourceDir, destinationDir string) error {
	var directories []directoryMode
	err := filepath.WalkDir(sourceDir, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
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
			if err := copyRegularFile(sourcePath, destinationPath, info.Mode()); err != nil {
				return err
			}
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(sourcePath)
			if err != nil {
				return fmt.Errorf("read symbolic link %q: %w", sourcePath, err)
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
		if err := os.Chmod(directories[index].path, directories[index].mode.Perm()); err != nil {
			return fmt.Errorf("preserve directory mode for %q: %w", directories[index].path, err)
		}
	}
	return nil
}

func copyRegularFile(sourcePath, destinationPath string, mode fs.FileMode) (err error) {
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

	if _, err := io.Copy(destination, source); err != nil {
		return fmt.Errorf("copy %q to %q: %w", sourcePath, destinationPath, err)
	}
	if err := destination.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("preserve file mode for %q: %w", destinationPath, err)
	}
	return nil
}
