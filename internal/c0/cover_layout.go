package c0

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/shrydev2020/gomcdc/v2/internal/processgroup"
)

// CoverLayout describes the coordinate, region, and statement-count rules
// observed from the selected go tool cover. It is determined from emitted
// counters, not a Go version allowlist.
type CoverLayout struct {
	PhysicalPositions    bool
	SplitCodeRanges      bool
	RangeStatementCounts bool
}

const coverLayoutProbe = "package p\n\n//line logical.go:100\nfunc F() {\n\t_ = 1\n\n\t_ = 2\n}\n"

// DetectCoverLayout checks the selected coverage producer without building or
// running the target. The fixture distinguishes physical coordinates and
// comment/blank-line splitting from logical basic-block coordinates, as well
// as whether split counters count statements per range or per basic block.
func DetectCoverLayout(ctx context.Context) (CoverLayout, error) {
	if err := ctx.Err(); err != nil {
		return CoverLayout{}, err
	}
	dir, err := os.MkdirTemp("", "gomcdc-cover-layout-")
	if err != nil {
		return CoverLayout{}, err
	}
	defer os.RemoveAll(dir)
	input, output := filepath.Join(dir, "probe.go"), filepath.Join(dir, "covered.go")
	if err := os.WriteFile(input, []byte(coverLayoutProbe), 0o600); err != nil {
		return CoverLayout{}, err
	}
	command := exec.CommandContext(ctx, "go", "tool", "cover", "-mode=set", "-var=GomcdcCoverLayout", "-o="+output, input)
	processgroup.ConfigureCancellation(command)
	if diagnostics, err := command.CombinedOutput(); err != nil {
		return CoverLayout{}, fmt.Errorf("inspect selected Go cover layout: %w: %s", err, diagnostics)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		return CoverLayout{}, err
	}
	positions, counts, err := coverMetadata(contents)
	if err != nil {
		return CoverLayout{}, err
	}
	return coverLayoutForMetadata(positions, counts)
}

func coverLayoutForMetadata(positions, counts []uint64) (CoverLayout, error) {
	for _, layout := range []CoverLayout{
		{},
		{PhysicalPositions: true, SplitCodeRanges: true},
		{PhysicalPositions: true, SplitCodeRanges: true, RangeStatementCounts: true},
	} {
		inventory, err := BuildProducerInventory("probe.go", []byte(coverLayoutProbe), layout)
		if err != nil {
			return CoverLayout{}, err
		}
		if matchesCoverMetadata(inventory, positions, counts) {
			return layout, nil
		}
	}
	return CoverLayout{}, fmt.Errorf("selected Go cover emitted an unrecognized layout: positions=%v statements=%v", positions, counts)
}

func coverMetadata(source []byte) ([]uint64, []uint64, error) {
	return namedCoverMetadata(source, "GomcdcCoverLayout")
}

// ValidateCoverMetadata checks the actual selected Go tool's counters against
// the planned producer inventory before accepting custom counter transport.
func ValidateCoverMetadata(source []byte, name string, inventory FileInventory) error {
	positions, counts, err := namedCoverMetadata(source, name)
	if err != nil {
		return err
	}
	if !matchesCoverMetadata(inventory, positions, counts) {
		return fmt.Errorf("Go cover metadata differs from planned test-file inventory")
	}
	return nil
}

func namedCoverMetadata(source []byte, variable string) ([]uint64, []uint64, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "covered.go", source, 0)
	if err != nil {
		return nil, nil, err
	}
	var positions, counts []uint64
	found := false
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, spec := range general.Specs {
			value := spec.(*ast.ValueSpec)
			if len(value.Names) != 1 || value.Names[0].Name != variable || len(value.Values) != 1 {
				continue
			}
			found = true
			literal, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, nil, fmt.Errorf("Go cover metadata is not a composite literal")
			}
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				name, ok := field.Key.(*ast.Ident)
				if !ok || name.Name != "Pos" && name.Name != "NumStmt" {
					continue
				}
				array, ok := field.Value.(*ast.CompositeLit)
				if !ok {
					return nil, nil, fmt.Errorf("Go cover %s is not an array", name.Name)
				}
				values := make([]uint64, 0, len(array.Elts))
				for _, expression := range array.Elts {
					integer, ok := expression.(*ast.BasicLit)
					if !ok || integer.Kind != token.INT {
						return nil, nil, fmt.Errorf("Go cover %s contains a non-integer", name.Name)
					}
					number, err := strconv.ParseUint(integer.Value, 0, 64)
					if err != nil {
						return nil, nil, err
					}
					values = append(values, number)
				}
				if name.Name == "Pos" {
					positions = values
				} else {
					counts = values
				}
			}
		}
	}
	if !found || len(positions) != len(counts)*3 {
		return nil, nil, fmt.Errorf("Go cover metadata arrays have inconsistent lengths")
	}
	return positions, counts, nil
}

func matchesCoverMetadata(inventory FileInventory, positions, counts []uint64) bool {
	if len(inventory.Blocks) != len(counts) || len(positions) != len(counts)*3 {
		return false
	}
	for index, block := range inventory.Blocks {
		profileRange := block.ProfileRange
		if positions[index*3] != uint64(profileRange.Start.Line) || positions[index*3+1] != uint64(profileRange.End.Line) ||
			positions[index*3+2] != uint64(profileRange.Start.Column)|uint64(profileRange.End.Column)<<16 ||
			counts[index] != uint64(block.coverStatements()) {
			return false
		}
	}
	return true
}
