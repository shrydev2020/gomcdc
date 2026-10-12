package report

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

// WriteHTMLDirectory renders bounded offline pages and publishes index.html
// after validating the complete generation. Existing generations remain readable.
func WriteHTMLDirectory(ctx context.Context, directory string, value analysis.Result, sources []analysis.SourceFileInput) (err error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start HTML report %q: %w", directory, err)
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("create HTML report directory %q: %w", directory, err)
	}
	stage, err := os.MkdirTemp(directory, ".gomcdc-html-")
	if err != nil {
		return fmt.Errorf("create HTML staging directory in %q: %w", directory, err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(stage); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove HTML staging directory %q: %w", stage, cleanupErr))
		}
	}()
	if err := renderHTMLDirectory(ctx, stage, value, sources); err != nil {
		return fmt.Errorf("render HTML report in %q: %w", stage, err)
	}
	if err := validateHTMLGeneration(ctx, stage); err != nil {
		return fmt.Errorf("validate HTML generation %q: %w", stage, err)
	}
	generation := "report-" + strings.TrimPrefix(filepath.Base(stage), ".gomcdc-html-")
	target := filepath.Join(directory, generation)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish HTML generation %q: %w", target, err)
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("publish HTML generation %q: %w", target, err)
	}
	overview := filepath.Join(target, "overview-1.html")
	entry, err := os.ReadFile(overview)
	if err != nil {
		return fmt.Errorf("read HTML overview %q: %w", overview, err)
	}
	// Generated hrefs are validated relative filenames; prefix them for the entry point.
	entry = bytes.ReplaceAll(entry, []byte(`href="`), []byte(`href="`+generation+`/`))
	if err := publishHTMLEntry(ctx, directory, entry); err != nil {
		return err
	}
	return nil
}

func publishHTMLEntry(ctx context.Context, directory string, content []byte) (err error) {
	f, err := os.CreateTemp(directory, ".index.html-")
	if err != nil {
		return fmt.Errorf("create HTML entry point in %q: %w", directory, err)
	}
	temp := f.Name()
	defer func() {
		if cleanupErr := os.Remove(temp); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary HTML entry %q: %w", temp, cleanupErr))
		}
	}()
	if _, writeErr := f.Write(content); writeErr != nil {
		err = fmt.Errorf("write HTML entry %q: %w", temp, writeErr)
	} else if syncErr := f.Sync(); syncErr != nil {
		err = fmt.Errorf("sync HTML entry %q: %w", temp, syncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close HTML entry %q: %w", temp, closeErr))
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(temp, 0644); err != nil {
		return fmt.Errorf("set HTML entry permissions %q: %w", temp, err)
	}
	target := filepath.Join(directory, "index.html")
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish HTML entry %q: %w", target, err)
	}
	if err := os.Rename(temp, target); err != nil {
		return fmt.Errorf("publish HTML entry %q: %w", target, err)
	}
	return nil
}

var htmlHref = regexp.MustCompile(`href="([^"]+)"`)

// validateHTMLGeneration checks asset types, page budgets and local links.
func validateHTMLGeneration(ctx context.Context, dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect generation directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("HTML generation %q must be a directory (mode %s)", dir, info.Mode())
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("visit HTML asset %q: %w", path, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate HTML asset %q: %w", path, err)
		}
		if path == dir {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("HTML asset %q must be a regular file (mode %s)", path, d.Type())
		}
		if strings.HasSuffix(d.Name(), ".html") {
			return validateHTMLPage(path)
		}
		return nil
	})
}

func validateHTMLPage(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect HTML page %q: %w", path, err)
	}
	if info.Size() > htmlPageBytes {
		return fmt.Errorf("HTML page %q exceeds %d bytes", path, htmlPageBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read HTML page %q: %w", path, err)
	}
	if htmlElements(string(content)) > htmlPageElements {
		return fmt.Errorf("HTML page %q exceeds %d elements", path, htmlPageElements)
	}
	for _, match := range htmlHref.FindAllSubmatch(content, -1) {
		dest, _, _ := strings.Cut(html.UnescapeString(string(match[1])), "#")
		if dest == "" || filepath.Base(dest) != dest {
			return fmt.Errorf("HTML page %q has invalid link %q", path, match[1])
		}
		linked, err := os.Lstat(filepath.Join(filepath.Dir(path), dest))
		if err != nil {
			return fmt.Errorf("resolve link %q from HTML page %q: %w", dest, path, err)
		}
		if !linked.Mode().IsRegular() {
			return fmt.Errorf("link %q from HTML page %q requires a regular file", dest, path)
		}
	}
	return nil
}
