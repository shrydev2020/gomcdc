package report_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/backend"
	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/report"
)

type schemaNode struct {
	Ref                  string                `json:"$ref"`
	Type                 json.RawMessage       `json:"type"`
	Const                any                   `json:"const"`
	Enum                 []any                 `json:"enum"`
	OneOf                []schemaNode          `json:"oneOf"`
	Items                *schemaNode           `json:"items"`
	Properties           map[string]schemaNode `json:"properties"`
	Required             []string              `json:"required"`
	AdditionalProperties json.RawMessage       `json:"additionalProperties"`
}

func TestCheckedInSchemaEnumsMatchPublicDomains(t *testing.T) {
	t.Parallel()
	document := readReportSchema(t)
	assertSchemaEnum(t, "resultStatus", document.Defs["resultStatus"], []string{
		string(report.ResultPassed), string(report.ResultFailed), string(report.ResultTimeout),
		string(report.ResultNotRun), string(report.ResultNotRequested),
	})
	assertSchemaEnum(t, "runStatus", document.Defs["runStatus"], []string{
		string(cover.RunPassed), string(cover.RunFailed), string(cover.RunTimeout),
	})
	assertSchemaEnum(t, "failureKind", document.Defs["failureKind"], []string{
		string(cover.RunFailureNone), string(cover.RunFailureBuild), string(cover.RunFailureTest),
		string(cover.RunFailureMixed), string(cover.RunFailureCommand), string(cover.RunFailureTimeout),
	})
	assertSchemaEnum(t, "packageStatus", document.Defs["packageStatus"], []string{
		string(gotest.PackageStarted), string(gotest.PackagePassed), string(gotest.PackageFailed),
		string(cover.RunTimeout), string(gotest.PackageBuildFailed), string(gotest.PackageSkipped),
	})
	assertSchemaEnum(t, "capabilityStatus", document.Defs["capabilityStatus"], []string{
		string(backend.CapabilitySupported), string(backend.CapabilityUnsupportedByBackend), string(backend.CapabilityUnknown),
	})
	assertSchemaEnum(t, "instrumentation metric", document.Defs["instrumentationMetric"].Properties["metric"], []string{
		string(config.MetricStatement), string(config.MetricFunction), string(config.MetricDecision),
		string(config.MetricSwitchClauseBody), string(config.MetricTypeSwitchClauseBody), string(config.MetricSelectClauseBody),
		string(config.MetricSwitchClauseSelection), string(config.MetricTypeSwitchClauseSelection),
		string(config.MetricCondition), string(config.MetricMCDCUnique), string(config.MetricMCDCMasking), "sourceAnalysis",
	})
	assertSchemaEnum(t, "decision kind", document.Defs["decisionReport"].Properties["kind"], []string{
		string(cover.DecisionIf), string(cover.DecisionFor), string(cover.DecisionSwitchCase),
	})
	assertSchemaEnum(t, "clause kind", document.Defs["clauseReport"].Properties["kind"], []string{
		string(cover.ClauseExpressionSwitch), string(cover.ClauseTypeSwitch), string(cover.ClauseSelect), string(cover.ClauseConditionlessSwitch),
	})
	assertSchemaEnum(t, "clause role", document.Defs["clauseReport"].Properties["role"], []string{
		string(cover.ClauseCase), string(cover.ClauseDefault),
	})
	assertSchemaEnum(t, "no-match kind", document.Defs["noMatchReport"].Properties["kind"], []string{
		string(cover.ClauseExpressionSwitch), string(cover.ClauseTypeSwitch),
	})
	assertSchemaEnum(t, "evaluation status", document.Defs["evaluationReport"].Properties["status"], []string{"completed", "aborted", "unknown"})
	assertSchemaEnum(t, "condition state", *document.Defs["evaluationReport"].Properties["conditions"].Items, []string{
		cover.ConditionNotEvaluated.String(), cover.ConditionFalse.String(), cover.ConditionTrue.String(),
	})
	assertSchemaEnum(t, "mcdc status", document.Defs["mcdcStatus"], []string{
		"", "disabled", string(cover.CoverageCovered), string(cover.CoverageNotCovered),
		string(cover.CoverageAnalysisIncomplete), string(cover.CoverageInfeasible),
		string(cover.SupportUnsupported), string(cover.SupportUnknown),
	})
	assertSchemaEnum(t, "mcdc outcome", document.Defs["mcdcOutcome"], []string{
		"", string(cover.CoverageOutcomeCovered), string(cover.CoverageOutcomeNotCovered), string(cover.CoverageOutcomeUnknown),
	})
	assertSchemaEnum(t, "support status", document.Defs["supportStatus"], []string{
		"", string(cover.SupportSupported), string(cover.SupportUnsupported), string(cover.SupportUnknown),
	})
	assertSchemaEnum(t, "analysis status", document.Defs["analysisStatus"], []string{
		"", string(cover.AnalysisComplete), string(cover.AnalysisIncomplete), string(cover.AnalysisInfeasible),
	})
}

type reportSchema struct {
	Schema string                `json:"$schema"`
	ID     string                `json:"$id"`
	Root   schemaNode            `json:"-"`
	Defs   map[string]schemaNode `json:"$defs"`
}

func (document *reportSchema) UnmarshalJSON(data []byte) error {
	type encoded reportSchema
	var fields struct {
		encoded
		schemaNode
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*document = reportSchema(fields.encoded)
	document.Root = fields.schemaNode
	return nil
}

func TestCheckedInSchemaMatchesPublicReportTypes(t *testing.T) {
	t.Parallel()
	document := readReportSchema(t)
	if document.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected JSON Schema dialect %q", document.Schema)
	}
	if document.Root.Properties["schemaVersion"].Const != report.SchemaVersion {
		t.Fatalf("schemaVersion const = %#v, want %q", document.Root.Properties["schemaVersion"].Const, report.SchemaVersion)
	}
	if err := checkSchemaShape(document, document.Root, reflect.TypeOf(report.Report{}), "report"); err != nil {
		t.Fatal(err)
	}

	capabilityDefinition := document.Defs["capabilitySet"]
	wantCapabilities := backend.OrchestratedBackend{}.Capabilities()
	if len(capabilityDefinition.Properties) != len(wantCapabilities) {
		t.Fatalf("schema capabilities = %d, implementation = %d", len(capabilityDefinition.Properties), len(wantCapabilities))
	}
	for capability := range wantCapabilities {
		if _, exists := capabilityDefinition.Properties[string(capability)]; !exists {
			t.Errorf("schema omits capability %q", capability)
		}
	}
}

func TestCheckedInSchemaDetectsContractDrift(t *testing.T) {
	t.Parallel()
	data := readReportSchemaJSON(t)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing public field", mutate: func(value map[string]any) {
			delete(value["properties"].(map[string]any), "toolVersion")
		}},
		{name: "invented public field", mutate: func(value map[string]any) {
			value["properties"].(map[string]any)["version"] = map[string]any{"type": "string"}
		}},
		{name: "wrong required set", mutate: func(value map[string]any) {
			value["required"] = []any{"schemaVersion"}
		}},
		{name: "wrong nullability", mutate: func(value map[string]any) {
			defs := value["$defs"].(map[string]any)
			metric := defs["metricSummary"].(map[string]any)
			metric["properties"].(map[string]any)["percentage"] = map[string]any{"type": "string"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := cloneSchemaJSON(t, data)
			test.mutate(mutated)
			encoded, err := json.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			var document reportSchema
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			if err := checkSchemaShape(&document, document.Root, reflect.TypeOf(report.Report{}), "report"); err == nil {
				t.Fatal("contract drift was not detected")
			}
		})
	}
}

func TestRenderedReportsCarrySchemaAndToolIdentities(t *testing.T) {
	t.Parallel()
	for _, input := range []report.Input{
		{ToolVersion: "v1.0.0", RunStatus: cover.RunPassed, FailureKind: cover.RunFailureNone, MeasurementMode: report.MeasurementSingleRun},
		{ToolVersion: "devel-abcdef012345-dirty", RunStatus: cover.RunFailed, FailureKind: cover.RunFailureTest, MeasurementMode: report.MeasurementDualRunStandardCover},
	} {
		encoded, err := report.RenderJSON(input)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		if value["schemaVersion"] != report.SchemaVersion || value["toolVersion"] != input.ToolVersion {
			t.Fatalf("report identities = schema %#v tool %#v", value["schemaVersion"], value["toolVersion"])
		}
	}
}

func readReportSchema(t *testing.T) *reportSchema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "schema", "report-v1.0.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document reportSchema
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse checked-in report schema: %v", err)
	}
	return &document
}

func readReportSchemaJSON(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "schema", "report-v1.0.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func cloneSchemaJSON(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func checkSchemaShape(document *reportSchema, node schemaNode, goType reflect.Type, path string) error {
	if node.Ref != "" {
		const prefix = "#/$defs/"
		if !strings.HasPrefix(node.Ref, prefix) {
			return fmt.Errorf("%s: unsupported reference %q", path, node.Ref)
		}
		definition, exists := document.Defs[strings.TrimPrefix(node.Ref, prefix)]
		if !exists {
			return fmt.Errorf("%s: missing definition %q", path, node.Ref)
		}
		return checkSchemaShape(document, definition, goType, path)
	}
	if goType.Kind() == reflect.Pointer {
		nonNull, nullable := nullableSchemaNode(node)
		if !nullable {
			return fmt.Errorf("%s: pointer field is not nullable", path)
		}
		return checkSchemaShape(document, nonNull, goType.Elem(), path)
	}

	schemaTypes, err := nodeTypes(node)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	switch goType.Kind() {
	case reflect.Struct:
		if !slices.Contains(schemaTypes, "object") {
			return fmt.Errorf("%s: Go struct requires schema object, got %v", path, schemaTypes)
		}
		return checkStructSchema(document, node, goType, path)
	case reflect.Slice:
		if !slices.Contains(schemaTypes, "array") || node.Items == nil {
			return fmt.Errorf("%s: Go slice requires schema array with items", path)
		}
		return checkSchemaShape(document, *node.Items, goType.Elem(), path+"[]")
	case reflect.Map:
		if !slices.Contains(schemaTypes, "object") || goType.Key().Kind() != reflect.String {
			return fmt.Errorf("%s: Go map requires schema object and string keys", path)
		}
		for name, property := range node.Properties {
			if err := checkSchemaShape(document, property, goType.Elem(), path+"."+name); err != nil {
				return err
			}
		}
		if len(node.Properties) == 0 && len(node.AdditionalProperties) > 0 && string(node.AdditionalProperties) != "false" {
			var additional schemaNode
			if err := json.Unmarshal(node.AdditionalProperties, &additional); err != nil {
				return fmt.Errorf("%s: invalid additionalProperties: %w", path, err)
			}
			return checkSchemaShape(document, additional, goType.Elem(), path+".*")
		}
		return nil
	case reflect.Bool:
		return requireSchemaType(path, schemaTypes, "boolean")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return requireSchemaType(path, schemaTypes, "integer")
	case reflect.Float32, reflect.Float64:
		return requireSchemaType(path, schemaTypes, "number")
	case reflect.String:
		if len(node.Enum) > 0 || node.Const != nil {
			return nil
		}
		return requireSchemaType(path, schemaTypes, "string")
	default:
		return fmt.Errorf("%s: unsupported Go kind %s", path, goType.Kind())
	}
}

func checkStructSchema(document *reportSchema, node schemaNode, goType reflect.Type, path string) error {
	if string(node.AdditionalProperties) != "false" {
		return fmt.Errorf("%s: public object must reject additional properties", path)
	}
	expectedProperties := make(map[string]reflect.StructField)
	var expectedRequired []string
	for index := 0; index < goType.NumField(); index++ {
		field := goType.Field(index)
		name, options := parseJSONTag(field)
		if name == "-" {
			continue
		}
		expectedProperties[name] = field
		if !slices.Contains(options, "omitempty") {
			expectedRequired = append(expectedRequired, name)
		}
	}
	if !sameStringSet(mapKeys(node.Properties), mapKeys(expectedProperties)) {
		return fmt.Errorf("%s: schema properties %v do not match Go fields %v", path, mapKeys(node.Properties), mapKeys(expectedProperties))
	}
	if !sameStringSet(node.Required, expectedRequired) {
		return fmt.Errorf("%s: required fields %v do not match Go fields %v", path, node.Required, expectedRequired)
	}
	for name, field := range expectedProperties {
		if err := checkSchemaShape(document, node.Properties[name], field.Type, path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

func parseJSONTag(field reflect.StructField) (string, []string) {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name, nil
	}
	parts := strings.Split(tag, ",")
	return parts[0], parts[1:]
}

func nullableSchemaNode(node schemaNode) (schemaNode, bool) {
	if types, _ := nodeTypes(node); slices.Contains(types, "null") {
		nonNullTypes := make([]string, 0, len(types)-1)
		for _, value := range types {
			if value != "null" {
				nonNullTypes = append(nonNullTypes, value)
			}
		}
		if len(nonNullTypes) == 1 {
			node.Type, _ = json.Marshal(nonNullTypes[0])
		} else {
			node.Type, _ = json.Marshal(nonNullTypes)
		}
		return node, true
	}
	for _, candidate := range node.OneOf {
		if types, _ := nodeTypes(candidate); !slices.Contains(types, "null") {
			return candidate, true
		}
	}
	return schemaNode{}, false
}

func nodeTypes(node schemaNode) ([]string, error) {
	if len(node.Type) > 0 {
		var single string
		if err := json.Unmarshal(node.Type, &single); err == nil {
			return []string{single}, nil
		}
		var multiple []string
		if err := json.Unmarshal(node.Type, &multiple); err != nil {
			return nil, fmt.Errorf("invalid schema type: %w", err)
		}
		return multiple, nil
	}
	if len(node.Enum) > 0 || node.Const != nil {
		return []string{"string"}, nil
	}
	return nil, nil
}

func requireSchemaType(path string, actual []string, expected string) error {
	if slices.Contains(actual, expected) {
		return nil
	}
	return fmt.Errorf("%s: schema types %v do not include %q", path, actual, expected)
}

func mapKeys[M ~map[string]V, V any](value M) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func sameStringSet(left, right []string) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func assertSchemaEnum(t *testing.T, name string, node schemaNode, want []string) {
	t.Helper()
	got := make([]string, 0, len(node.Enum))
	for _, value := range node.Enum {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("%s enum contains non-string value %#v", name, value)
		}
		got = append(got, text)
	}
	if !sameStringSet(got, want) {
		t.Fatalf("%s enum = %v, want %v", name, got, want)
	}
}
