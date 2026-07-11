package c0map

import (
	"reflect"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/c0"
)

func TestBuildMapsLogicalAbsoluteAndGeneratedPaths(t *testing.T) {
	t.Parallel()
	profile := c0.Profile{Mode: c0.ModeSet, Files: []c0.ProfileFile{
		{Path: "/tmp/work/module/internal/p/p.go"},
		{Path: "example.test/project/root.go"},
		{Path: "gocoverage-generated/internal_p_p.go"},
		{Path: "external.test/dependency/value.go"},
	}}
	result, err := Build(profile, "example.test/project", []Source{
		{PackagePath: "example.test/project/internal/p", RelativePath: "internal/p/p.go", OriginalSource: []byte("package p\n")},
		{PackagePath: "example.test/project", RelativePath: "root.go", OriginalSource: []byte("package project\n")},
	}, []GeneratedFile{{Path: "gocoverage-generated/internal_p_p.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.Files), 3; got != want {
		t.Fatalf("mapping count = %d, want %d: %#v", got, want, result.Files)
	}
	if result.Files[0].OriginalPath != "internal/p/p.go" || result.Files[1].OriginalPath != "root.go" || !result.Files[2].Generated {
		t.Fatalf("mappings = %#v", result.Files)
	}
}

func TestBuildIsDeterministicAndCopiesSource(t *testing.T) {
	t.Parallel()
	source := []byte("package p\n")
	profile := c0.Profile{Mode: c0.ModeSet, Files: []c0.ProfileFile{{Path: "m/p.go"}}}
	first, err := Build(profile, "m", []Source{{PackagePath: "m", RelativePath: "p.go", OriginalSource: source}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	source[0] = 'X'
	if reflect.DeepEqual(first.Files[0].OriginalSource, source) {
		t.Fatal("source map retained caller-owned source bytes")
	}
}

func TestBuildRejectsAmbiguousSuffix(t *testing.T) {
	t.Parallel()
	profile := c0.Profile{Mode: c0.ModeSet, Files: []c0.ProfileFile{{Path: "/tmp/p/value.go"}}}
	_, err := Build(profile, "m", []Source{
		{PackagePath: "m/a", RelativePath: "p/value.go"},
		{PackagePath: "m/b", RelativePath: "p/value.go"},
	}, nil)
	if err == nil {
		t.Fatal("Build() error = nil, want ambiguity error")
	}
}

func TestBuildPrefersExactModulePathOverShorterSuffix(t *testing.T) {
	t.Parallel()

	profile := c0.Profile{Mode: c0.ModeSet, Files: []c0.ProfileFile{{Path: "m/nested/p/x.go"}}}
	result, err := Build(profile, "m", []Source{
		{PackagePath: "m/p", RelativePath: "p/x.go", OriginalSource: []byte("package p\n")},
		{PackagePath: "m/nested/p", RelativePath: "nested/p/x.go", OriginalSource: []byte("package p\n")},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, mapping := range result.Files {
		if mapping.ProfilePath != profile.Files[0].Path {
			continue
		}
		if got, want := mapping.OriginalPath, "nested/p/x.go"; got != want {
			t.Fatalf("exact profile mapping = %q, want %q", got, want)
		}
		return
	}
	t.Fatalf("profile mapping absent: %#v", result.Files)
}

func TestBuildUsesOriginalInventoryAsDenominator(t *testing.T) {
	t.Parallel()

	const source = `package p

func F(value bool) int {
	result := 0
	if value {
		result = 1
	}
	return result
}
`
	inventory, err := c0.BuildInventory("p/p.go", []byte(source))
	if err != nil {
		t.Fatalf("BuildInventory: %v", err)
	}
	profile := c0.Profile{Mode: c0.ModeSet, Files: []c0.ProfileFile{{Path: "example.test/m/p/p.go"}}}
	wantStatements := 0
	wantCovered := 0
	blockIndex := 0
	for _, block := range inventory.Blocks {
		if block.Statements == 0 {
			continue
		}
		wantStatements += block.Statements
		count := uint64(blockIndex % 2)
		blockIndex++
		if count > 0 {
			wantCovered += block.Statements
		}
		profile.Files[0].Blocks = append(profile.Files[0].Blocks, c0.ProfileBlock{
			Position: c0.SourceRange{
				Start: c0.Position{Line: block.ProfileRange.Start.Line},
				End:   c0.Position{Line: block.ProfileRange.End.Line},
			},
			Statements: block.Statements + 7,
			Count:      count,
		})
	}
	sourceMap, err := Build(profile, "example.test/m", []Source{{
		PackagePath: "example.test/m/p", RelativePath: "p/p.go", OriginalSource: []byte(source),
	}}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	report, err := c0.Analyze(profile, sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := report.Summary.Statements; got != (c0.Counts{Covered: wantCovered, Total: wantStatements}) {
		t.Fatalf("statement summary = %#v, want original %d/%d", got, wantCovered, wantStatements)
	}
	if !report.Packages[0].Evidence || !report.Packages[0].Files[0].Evidence {
		t.Fatalf("profile-backed report lost evidence: %#v", report.Packages[0])
	}
}

func TestBuildRetainsInventoryWhenProfileIsPartial(t *testing.T) {
	t.Parallel()

	const source = "package p\nfunc Known() {\n\tprintln(\"known\")\n}\n"
	sourceMap, err := Build(c0.Profile{Mode: c0.ModeSet}, "example.test/m", []Source{{
		PackagePath: "example.test/m/p", RelativePath: "p/p.go", OriginalSource: []byte(source),
	}}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := len(sourceMap.Files), 1; got != want {
		t.Fatalf("inventory-only mapping count = %d, want %d", got, want)
	}
	report, err := c0.Analyze(c0.Profile{Mode: c0.ModeSet}, sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := report.Summary.Statements; got != (c0.Counts{Total: 1}) {
		t.Fatalf("static statement summary = %#v, want one known uncovered unit", got)
	}
	packageReport := report.Packages[0]
	if packageReport.Evidence || packageReport.Files[0].Evidence || packageReport.Files[0].Functions[0].Evidence {
		t.Fatalf("inventory-only hierarchy reported evidence: %#v", packageReport)
	}
	if packageReport.Files[0].Functions[0].Blocks[0].Evidence {
		t.Fatalf("inventory-only statement reported evidence")
	}
}

func TestBuildMapsLineDirectiveProfileToPhysicalOriginal(t *testing.T) {
	t.Parallel()

	source := []byte("package p\n\n//line virtual.go:100\nfunc F(value bool) int {\n\tif value {\n\t\treturn 1\n\t}\n\treturn 0\n}\n")
	inventory, err := c0.BuildInventory("p/original.go", source)
	if err != nil {
		t.Fatalf("BuildInventory: %v", err)
	}
	profile := c0.Profile{Mode: c0.ModeSet, Files: []c0.ProfileFile{{Path: "example.test/m/p/virtual.go"}}}
	for _, block := range inventory.Blocks {
		if block.Statements > 0 {
			profile.Files[0].Blocks = append(profile.Files[0].Blocks, c0.ProfileBlock{
				Position: block.ProfileRange, Statements: block.Statements, Count: 1,
			})
		}
	}
	sourceMap, err := Build(profile, "example.test/m", []Source{{
		PackagePath: "example.test/m/p", RelativePath: "p/original.go", OriginalSource: source,
	}}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	report, err := c0.Analyze(profile, sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	file := report.Packages[0].Files[0]
	if file.Path != "p/original.go" || !file.Evidence {
		t.Fatalf("mapped file = %#v", file)
	}
	for _, function := range file.Functions {
		for _, block := range function.Blocks {
			if block.Position.Start.Line >= 100 {
				t.Fatalf("reported logical instead of physical range: %#v", block.Position)
			}
		}
	}
}

func TestBuildRetainsUserGeneratedSourceInventory(t *testing.T) {
	t.Parallel()

	const generated = "// Code generated by fixture. DO NOT EDIT.\npackage p\nfunc Generated() { println(\"x\") }\n"
	sourceMap, err := Build(c0.Profile{Mode: c0.ModeSet}, "example.test/m", []Source{{
		PackagePath: "example.test/m/p", RelativePath: "p/generated.go", OriginalSource: []byte(generated),
	}}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(sourceMap.Files) != 1 || sourceMap.Files[0].Generated || sourceMap.Files[0].Inventory == nil {
		t.Fatalf("generated user source inventory mapping = %#v", sourceMap.Files)
	}
}
