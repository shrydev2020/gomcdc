package measurement

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Follow every reachable wire structure once, including recursive Boolean
// trees. Keep the checked-in contract in sync without executing Go tests in a
// target checkout or coupling the codec to a schema library.
func TestArchiveSchemaDeclaresWireFields(t *testing.T) {
	data, err := os.ReadFile("../../schema/measurement-v1.0.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			visit(typ.Elem())
			return
		case reflect.Struct:
		default:
			return
		}
		name := strings.TrimPrefix(typ.PkgPath(), "github.com/shrydev2020/gomcdc/v2/internal/") + "." + typ.Name()
		// Probes is declared in the common coverage domain and reexported here.
		if name == "coverage.Probes" {
			name = "measurement.Probes"
		}
		var node struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(document.Defs[name], &node); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if key == "-" {
				continue
			}
			if key == "" {
				key = f.Name
			}
			if _, ok := node.Properties[key]; !ok {
				t.Errorf("missing schema property %s.%s", name, key)
			}
			delete(node.Properties, key)
			visit(f.Type)
		}
		if typ == reflect.TypeFor[Plan]() {
			if _, ok := node.Properties["byteRanges"]; !ok {
				t.Fatal("schema omits source byte ranges")
			}
			delete(node.Properties, "byteRanges")
		}
		if len(node.Properties) > 0 {
			t.Errorf("schema has obsolete fields for %s: %v", name, node.Properties)
		}
	}
	visit(reflect.TypeFor[Archive]())
}
