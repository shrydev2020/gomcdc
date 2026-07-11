package c0_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/c0"
)

func TestAnalyzeMultiPackageIdentityMappingsAndCounts(t *testing.T) {
	t.Parallel()

	profile := `mode: count
/tmp/ws/pkg/b/b.go:3.1,3.13 2 0
/tmp/ws/pkg/a/a.go:4.1,4.6 1 0
/tmp/ws/pkg/a/a.go:3.1,3.7 1 3
`
	sourceMap := c0.SourceMap{
		ModulePath: "example.com/m",
		Files: []c0.FileMapping{
			{
				ProfilePath: "/tmp/ws/pkg/b/b.go", PackagePath: "example.com/m/pkg/b",
				OriginalPath: "pkg/b/b.go", OriginalSource: []byte("package b\nfunc B() {\nprintln(\"b\")\n}\n"),
			},
			{
				ProfilePath: "/tmp/ws/pkg/a/a.go", PackagePath: "example.com/m/pkg/a",
				OriginalPath: "pkg/a/a.go", OriginalSource: []byte("package a\nfunc A() {\nx := 1\n_ = x\n}\n"),
			},
		},
	}

	report, err := c0.ParseAndAnalyze(strings.NewReader(profile), sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("ParseAndAnalyze: %v", err)
	}
	assertSummary(t, "module", report.Summary, c0.Counts{Covered: 1, Total: 4}, c0.Counts{Covered: 1, Total: 3}, c0.Counts{Covered: 1, Total: 2})
	if got, want := packagePaths(report), []string{"example.com/m/pkg/a", "example.com/m/pkg/b"}; !equalStrings(got, want) {
		t.Fatalf("package order = %v, want %v", got, want)
	}
	assertSummary(t, "package a", report.Packages[0].Summary, c0.Counts{Covered: 1, Total: 2}, c0.Counts{Covered: 1, Total: 2}, c0.Counts{Covered: 1, Total: 1})
	assertSummary(t, "package b", report.Packages[1].Summary, c0.Counts{Covered: 0, Total: 2}, c0.Counts{Covered: 0, Total: 1}, c0.Counts{Covered: 0, Total: 1})
	if got, want := report.Packages[0].Files[0].Path, "pkg/a/a.go"; got != want {
		t.Fatalf("original file path = %q, want %q", got, want)
	}
	if got, want := len(report.Packages[0].Files[0].Functions[0].Blocks), 2; got != want {
		t.Fatalf("A block count = %d, want %d", got, want)
	}
	if len(report.Excluded) != 0 {
		t.Fatalf("excluded = %#v, want none", report.Excluded)
	}
}

func TestAnalyzeBlockOverridesAndExposesGeneratedOrUnmappableData(t *testing.T) {
	t.Parallel()

	profile := `mode: set
/tmp/user.go:10.2,10.9 1 1
/tmp/user.go:11.1,11.5 1 1
/tmp/user.go:3.9,3.11 1 1
/tmp/bridge.go:2.1,2.8 1 1
/tmp/unknown.go:1.1,1.2 1 0
`
	acceptedProfileRange := sourceRange(10, 2, 10, 9)
	acceptedOriginalRange := sourceRange(3, 1, 3, 14)
	generatedProfileRange := sourceRange(11, 1, 11, 5)
	sourceMap := c0.SourceMap{
		ModulePath: "example.com/m",
		Files: []c0.FileMapping{
			{
				ProfilePath: "/tmp/user.go", PackagePath: "example.com/m/p", OriginalPath: "p/p.go",
				OriginalSource: []byte("package p\nfunc F() {\nprintln(\"ok\")\n}\n"),
				Blocks: []c0.BlockMapping{
					{ProfileRange: acceptedProfileRange, OriginalRange: acceptedOriginalRange},
					{ProfileRange: generatedProfileRange, Generated: true},
				},
			},
			{ProfilePath: "/tmp/bridge.go", Generated: true},
		},
	}

	report, err := c0.ParseAndAnalyze(strings.NewReader(profile), sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("ParseAndAnalyze: %v", err)
	}
	assertSummary(t, "module", report.Summary, c0.Counts{Covered: 1, Total: 1}, c0.Counts{Covered: 1, Total: 1}, c0.Counts{Covered: 1, Total: 1})
	block := report.Packages[0].Files[0].Functions[0].Blocks[0]
	if !reflect.DeepEqual(block.Position, acceptedOriginalRange) {
		t.Fatalf("mapped block position = %#v, want %#v", block.Position, acceptedOriginalRange)
	}

	wantReasons := map[c0.ExclusionReason]int{
		c0.ExcludeGeneratedFile:       1,
		c0.ExcludeGeneratedBlock:      1,
		c0.ExcludeUnmappedFile:        1,
		c0.ExcludeNoOriginalStatement: 1,
	}
	gotReasons := make(map[c0.ExclusionReason]int)
	for _, excluded := range report.Excluded {
		gotReasons[excluded.Reason]++
		if excluded.ProfilePath == "" {
			t.Fatalf("excluded block lost profile path: %#v", excluded)
		}
	}
	if !reflect.DeepEqual(gotReasons, wantReasons) {
		t.Fatalf("excluded reasons = %v, want %v", gotReasons, wantReasons)
	}
}

func TestAnalyzeEmptyFunctionsDefaultAndIncluded(t *testing.T) {
	t.Parallel()

	profile := "mode: atomic\n/tmp/p.go:4.1,4.13 1 7\n"
	sourceMap := c0.SourceMap{
		ModulePath: "example.com/m",
		Files: []c0.FileMapping{
			{
				ProfilePath: "/tmp/p.go", PackagePath: "example.com/m/p", OriginalPath: "p/p.go",
				OriginalSource: []byte("package p\nfunc Empty() {}\nfunc NonEmpty() {\nprintln(\"x\")\n}\n"),
			},
			{
				ProfilePath: "/tmp/empty.go", PackagePath: "example.com/m/p", OriginalPath: "p/empty.go",
				OriginalSource: []byte("package p\nfunc OnlyEmpty() {}\n"),
			},
		},
	}

	defaultReport, err := c0.ParseAndAnalyze(strings.NewReader(profile), sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("default ParseAndAnalyze: %v", err)
	}
	if got, want := functionNames(defaultReport), []string{"NonEmpty"}; !equalStrings(got, want) {
		t.Fatalf("default functions = %v, want %v", got, want)
	}
	assertCounts(t, "default functions", defaultReport.Summary.Functions, c0.Counts{Covered: 1, Total: 1})

	includedReport, err := c0.ParseAndAnalyze(strings.NewReader(profile), sourceMap, c0.Options{IncludeEmptyFunctions: true})
	if err != nil {
		t.Fatalf("included ParseAndAnalyze: %v", err)
	}
	if got, want := functionNames(includedReport), []string{"OnlyEmpty", "Empty", "NonEmpty"}; !equalStrings(got, want) {
		t.Fatalf("included functions = %v, want %v", got, want)
	}
	assertCounts(t, "included functions", includedReport.Summary.Functions, c0.Counts{Covered: 1, Total: 3})
	if got := includedReport.Packages[0].Files[0].Functions[0].Summary.Statements; got != (c0.Counts{}) {
		t.Fatalf("empty function statements = %#v, want zero", got)
	}
}

func TestAnalyzeAssignsNestedLiteralStatementsToInnermostFunction(t *testing.T) {
	t.Parallel()

	profile := `mode: count
/tmp/p.go:6.1,6.4 1 0
/tmp/p.go:4.1,4.17 1 0
/tmp/p.go:3.1,3.12 1 1
`
	sourceMap := c0.SourceMap{
		ModulePath: "example.com/m",
		Files: []c0.FileMapping{
			{
				ProfilePath: "/tmp/p.go", PackagePath: "example.com/m/p", OriginalPath: "p/p.go",
				OriginalSource: []byte("package p\nfunc Outer() {\nf := func() {\nprintln(\"inner\")\n}\nf()\n}\n"),
			},
		},
	}

	report, err := c0.ParseAndAnalyze(strings.NewReader(profile), sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("ParseAndAnalyze: %v", err)
	}
	functions := report.Packages[0].Files[0].Functions
	if got, want := []string{functions[0].Name, functions[1].Name}, []string{"Outer", "Outer.func@3:6"}; !equalStrings(got, want) {
		t.Fatalf("functions = %v, want %v", got, want)
	}
	assertSummary(t, "outer", functions[0].Summary, c0.Counts{Covered: 1, Total: 2}, c0.Counts{Covered: 1, Total: 2}, c0.Counts{Covered: 1, Total: 1})
	assertSummary(t, "literal", functions[1].Summary, c0.Counts{Covered: 0, Total: 1}, c0.Counts{Covered: 0, Total: 1}, c0.Counts{Covered: 0, Total: 1})
	assertCounts(t, "module functions", report.Summary.Functions, c0.Counts{Covered: 1, Total: 2})
}

func TestAnalyzeIsDeterministicAcrossInputOrder(t *testing.T) {
	t.Parallel()

	profile := c0.Profile{
		Mode: c0.ModeSet,
		Files: []c0.ProfileFile{
			{Path: "/tmp/z.go", Blocks: []c0.ProfileBlock{{Position: sourceRange(3, 1, 3, 4), Statements: 1, Count: 0}}},
			{Path: "/tmp/a.go", Blocks: []c0.ProfileBlock{{Position: sourceRange(3, 1, 3, 4), Statements: 1, Count: 1}}},
		},
	}
	sourceMap := c0.SourceMap{
		ModulePath: "example.com/m",
		Files: []c0.FileMapping{
			{ProfilePath: "/tmp/z.go", PackagePath: "z", OriginalPath: "z.go", OriginalSource: []byte("package z\nfunc Z() {\nZed()\n}\n")},
			{ProfilePath: "/tmp/a.go", PackagePath: "a", OriginalPath: "a.go", OriginalSource: []byte("package a\nfunc A() {\nAct()\n}\n")},
		},
	}
	first, err := c0.Analyze(profile, sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("first Analyze: %v", err)
	}
	profile.Files[0], profile.Files[1] = profile.Files[1], profile.Files[0]
	sourceMap.Files[0], sourceMap.Files[1] = sourceMap.Files[1], sourceMap.Files[0]
	second, err := c0.Analyze(profile, sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("second Analyze: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Analyze depends on input order\nfirst: %#v\nsecond: %#v", first, second)
	}
}

func TestAnalyzeGeneratedOriginalSourceIsCovered(t *testing.T) {
	t.Parallel()

	profile := "mode: set\n/tmp/generated.go:4.1,4.9 1 1\n"
	sourceMap := c0.SourceMap{
		ModulePath: "example.com/m",
		Files: []c0.FileMapping{
			{
				ProfilePath: "/tmp/generated.go", PackagePath: "example.com/m/p", OriginalPath: "p/generated.go",
				OriginalSource: []byte("// Code generated by test. DO NOT EDIT.\npackage p\nfunc G() {\nwork()\n}\n"),
			},
		},
	}
	report, err := c0.ParseAndAnalyze(strings.NewReader(profile), sourceMap, c0.Options{})
	if err != nil {
		t.Fatalf("ParseAndAnalyze: %v", err)
	}
	if report.Summary.Statements != (c0.Counts{Covered: 1, Total: 1}) || report.Summary.Functions != (c0.Counts{Covered: 1, Total: 1}) {
		t.Fatalf("generated source coverage = %#v", report)
	}
	if len(report.Packages) != 1 || len(report.Excluded) != 0 {
		t.Fatalf("generated source hierarchy/exclusions = packages:%#v excluded:%#v", report.Packages, report.Excluded)
	}
}

func sourceRange(startLine, startColumn, endLine, endColumn int) c0.SourceRange {
	return c0.SourceRange{
		Start: c0.Position{Line: startLine, Column: startColumn},
		End:   c0.Position{Line: endLine, Column: endColumn},
	}
}

func packagePaths(report c0.Report) []string {
	paths := make([]string, 0, len(report.Packages))
	for _, packageReport := range report.Packages {
		paths = append(paths, packageReport.Path)
	}
	return paths
}

func functionNames(report c0.Report) []string {
	names := make([]string, 0)
	for _, packageReport := range report.Packages {
		for _, fileReport := range packageReport.Files {
			for _, function := range fileReport.Functions {
				names = append(names, function.Name)
			}
		}
	}
	return names
}

func assertSummary(t *testing.T, name string, got c0.Summary, statements, blocks, functions c0.Counts) {
	t.Helper()
	assertCounts(t, name+" statements", got.Statements, statements)
	assertCounts(t, name+" blocks", got.Blocks, blocks)
	assertCounts(t, name+" functions", got.Functions, functions)
}

func assertCounts(t *testing.T, name string, got, want c0.Counts) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}
