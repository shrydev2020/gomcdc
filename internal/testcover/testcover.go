// Package testcover extends Go's cover instrumentation to active _test.go files.
// Counter placement and metadata come from the selected go tool cover. Only
// transport differs: synchronous, idempotent byte writes survive process exit.
package testcover

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/c0"
	"github.com/shrydev2020/gomcdc/v2/internal/processgroup"
)

const DataDirEnv = "GOMCDC_TEST_COVER_DIR"
const RunIDEnv = "GOMCDC_TEST_COVER_RUN"
const counterPrefix = "gomcdc-test-cover-"

type Source struct {
	PackagePath  string
	RelativePath string
	CopyPath     string
}

type File struct {
	Source
	ID        string
	Inventory c0.FileInventory
}

type Session struct{ Files []File }

// Instrument runs after AST rewriting, whose correspondence still describes
// the source observed by Go cover. Generated cover declarations are not part
// of that denominator.
func Instrument(ctx context.Context, moduleDir, modulePath string, sources []Source) (Session, error) {
	result := Session{}
	if len(sources) == 0 {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	layout, err := c0.DetectCoverLayout(ctx)
	if err != nil {
		return result, err
	}
	runtimeDir, err := os.MkdirTemp(moduleDir, "gomcdc_testcover_")
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime.go"), []byte(runtimeSource), 0o600); err != nil {
		return result, err
	}
	runtimeImport := modulePath + "/" + filepath.Base(runtimeDir)
	namesByDirectory := map[string]map[string]bool{}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		contents, err := os.ReadFile(source.CopyPath)
		if err != nil {
			return result, err
		}
		inventory, err := c0.BuildProducerInventory(source.RelativePath, contents, layout)
		if err != nil {
			return result, err
		}
		id := fmt.Sprintf("%x", sha256.Sum256(append([]byte(source.PackagePath+"\x00"+source.RelativePath+"\x00"), contents...)))
		directory := filepath.Dir(source.CopyPath)
		names := namesByDirectory[directory]
		if names == nil {
			names, err = helperNames(directory)
			if err != nil {
				return result, err
			}
			namesByDirectory[directory] = names
		}
		name := reserveHelperName(names)
		output := filepath.Join(runtimeDir, "covered.tmp")
		command := exec.CommandContext(ctx, "go", "tool", "cover", "-mode=set", "-var="+name, "-o="+output, source.CopyPath)
		processgroup.ConfigureCancellation(command)
		if diagnostic, err := command.CombinedOutput(); err != nil {
			return result, fmt.Errorf("instrument test cover %q: %w: %s", source.RelativePath, err, diagnostic)
		}
		covered, err := os.ReadFile(output)
		if err != nil {
			return result, err
		}
		if err := c0.ValidateCoverMetadata(covered, name, inventory); err != nil {
			return result, fmt.Errorf("test cover %q: %w", source.RelativePath, err)
		}
		rewritten, err := rewriteCounters(covered, name, runtimeImport, id, len(inventory.Blocks))
		if err != nil {
			return result, err
		}
		if err := replaceSource(ctx, source.CopyPath, rewritten); err != nil {
			return result, err
		}
		result.Files = append(result.Files, File{Source: source, ID: id, Inventory: inventory})
	}
	return result, nil
}

// Scan all Go identifiers, including inactive/malformed files, so neither the
// package variable nor the file import can shadow a user identifier.
func helperNames(directory string) (map[string]bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		set := token.NewFileSet()
		var scan scanner.Scanner
		scan.Init(set.AddFile(entry.Name(), -1, len(contents)), contents, nil, 0)
		for {
			_, tok, literal := scan.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.IDENT {
				names[literal] = true
			}
		}
	}
	return names, nil
}

func reserveHelperName(names map[string]bool) string {
	for index := 0; ; index++ {
		name := fmt.Sprintf("__gomcdcTestCover%d", index)
		if !names[name] && !names[name+"Runtime"] {
			names[name], names[name+"Runtime"] = true, true
			return name
		}
	}
}

type edit struct {
	start, end int
	text       string
}

func rewriteCounters(source []byte, name, runtimeImport, id string, count int) ([]byte, error) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "covered.go", source, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	offset := func(pos token.Pos) int { return set.PositionFor(pos, false).Offset }
	edits := []edit{{offset(file.Name.End()), offset(file.Name.End()), "\nimport " + name + "Runtime " + strconv.Quote(runtimeImport) + "\n"}}
	seen := make([]bool, count)
	var invalid error
	ast.Inspect(file, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 {
			return true
		}
		index, ok := assignment.Lhs[0].(*ast.IndexExpr)
		if !ok {
			return true
		}
		selector, ok := index.X.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Count" {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || receiver.Name != name {
			return true
		}
		literal, ok := index.Index.(*ast.BasicLit)
		if !ok {
			invalid = fmt.Errorf("unexpected Go cover index")
			return false
		}
		number, err := strconv.Atoi(literal.Value)
		if err != nil || number < 0 || number >= count || seen[number] {
			invalid = fmt.Errorf("invalid or duplicate Go cover index %q", literal.Value)
			return false
		}
		if assignment.Tok != token.ASSIGN || len(assignment.Rhs) != 1 {
			invalid = fmt.Errorf("unexpected Go cover assignment")
			return false
		}
		value, ok := assignment.Rhs[0].(*ast.BasicLit)
		if !ok || value.Value != "1" {
			invalid = fmt.Errorf("unexpected Go cover value")
			return false
		}
		seen[number] = true
		edits = append(edits, edit{offset(assignment.Pos()), offset(assignment.End()), fmt.Sprintf("%s.Hit(%d)", name, number)})
		return false
	})
	if invalid != nil {
		return nil, invalid
	}
	if slices.Contains(seen, false) {
		return nil, fmt.Errorf("Go cover emitted counters without probes")
	}
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR || len(general.Specs) != 1 {
			continue
		}
		value := general.Specs[0].(*ast.ValueSpec)
		if len(value.Names) == 1 && value.Names[0].Name == name {
			edits = append(edits, edit{offset(general.Pos()), offset(general.End()), fmt.Sprintf("var %s = %sRuntime.NewFile(%q, %d)", name, name, id, count)})
		}
	}
	slices.SortFunc(edits, func(a, b edit) int { return cmp.Compare(a.start, b.start) })
	result := make([]byte, 0, len(source))
	position := 0
	for _, edit := range edits {
		if edit.start < position {
			return nil, fmt.Errorf("overlapping Go cover edits")
		}
		result = append(result, source[position:edit.start]...)
		result = append(result, edit.text...)
		position = edit.end
	}
	return append(result, source[position:]...), nil
}

// Collect verifies session/file identity and exact counter shape before
// projecting bytes into Go cover regions. Missing files remain missing evidence;
// they never become fabricated zero counts after an interrupted test run.
func (session Session) Collect(ctx context.Context, dataDir, runID string) ([]c0.ProfileFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(session.Files) == 0 {
		return nil, nil
	}
	expected := make(map[string]File, len(session.Files))
	for _, file := range session.Files {
		expected[file.ID] = file
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	counts := map[string][]byte{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(entry.Name(), counterPrefix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dataDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		header, counters, ok := bytes.Cut(data, []byte("\n"))
		fields := strings.Split(string(header), " ")
		if !ok || len(fields) != 3 || fields[0] != "gomcdc-test-cover-v1" || fields[1] != runID {
			return nil, fmt.Errorf("invalid test cover identity in %q", entry.Name())
		}
		file, ok := expected[fields[2]]
		if !ok || len(counters) != len(file.Inventory.Blocks) {
			return nil, fmt.Errorf("invalid test cover shape in %q", entry.Name())
		}
		merged := counts[file.ID]
		if merged == nil {
			merged = make([]byte, len(counters))
		}
		for index, value := range counters {
			if value > 1 {
				return nil, fmt.Errorf("invalid test cover counter in %q", entry.Name())
			}
			merged[index] |= value
		}
		counts[file.ID] = merged
	}
	var profiles []c0.ProfileFile
	for _, file := range session.Files {
		counters, observed := counts[file.ID]
		if !observed {
			continue
		}
		byPath := map[string][]c0.ProfileBlock{}
		for index, block := range file.Inventory.Blocks {
			statements := block.Statements
			if block.CoverStatements != 0 {
				statements = block.CoverStatements
			}
			byPath[block.ProfileFile] = append(byPath[block.ProfileFile], c0.ProfileBlock{Position: block.ProfileRange, Statements: statements, Count: uint64(counters[index])})
		}
		for _, path := range slices.Sorted(maps.Keys(byPath)) {
			profiles = append(profiles, c0.ProfileFile{Path: path, Blocks: byPath[path]})
		}
	}
	return profiles, nil
}

// Replacing the copied inode also supports read-only source files and preserves
// their mode, matching the AST instrumentation's workspace write behavior.
func replaceSource(ctx context.Context, path string, source []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".gomcdc-test-cover-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	_, writeErr := temporary.Write(source)
	if err := errors.Join(writeErr, temporary.Chmod(info.Mode().Perm()), temporary.Close()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
