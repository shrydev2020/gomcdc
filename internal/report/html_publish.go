package report

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"io"
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
	digest, err := htmlGenerationDigest(ctx, stage)
	if err != nil {
		return fmt.Errorf("validate HTML generation %q: %w", stage, err)
	}
	generation := "report-" + digest
	target := filepath.Join(directory, generation)
	if err := publishHTMLGeneration(ctx, stage, target, digest); err != nil {
		return err
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

func publishHTMLGeneration(ctx context.Context, stage, target, digest string) error {
	if _, err := os.Lstat(target); err == nil {
		existing, err := htmlGenerationDigest(ctx, target)
		if err != nil {
			return fmt.Errorf("validate existing HTML generation %q: %w", target, err)
		}
		if existing != digest {
			return fmt.Errorf("HTML generation %q has digest %s; expected %s", target, existing, digest)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect HTML generation %q: %w", target, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish HTML generation %q: %w", target, err)
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("publish HTML generation %q: %w", target, err)
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

// htmlGenerationDigest validates and hashes each asset in filename order.
func htmlGenerationDigest(ctx context.Context, dir string) (string, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("inspect generation directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("HTML generation %q must be a directory (mode %s)", dir, info.Mode())
	}
	h := sha256.New()
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
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
		digest, err := htmlAssetDigest(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%d:%s:%s\n", len(d.Name()), d.Name(), digest)
		if strings.HasSuffix(d.Name(), ".html") {
			if err := validateHTMLPage(path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func htmlAssetDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open HTML asset %q: %w", path, err)
	}
	h := sha256.New()
	if _, copyErr := io.Copy(h, f); copyErr != nil {
		err = fmt.Errorf("hash HTML asset %q: %w", path, copyErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close HTML asset %q: %w", path, closeErr))
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
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
