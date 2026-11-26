package factorymanifest

import (
	"reflect"
	"strings"
	"testing"
)

const sample = `{"schema": "factory.api-manifest/v1", "adapter": "go", "project": ".", "apis": [
  {"id": "a.gen", "name": "Gen", "domain": "a", "source": {"path": "design/a/Gen.yaml"},
   "rules": [{"path": "design/a/Gen.yaml", "line": 7, "when": "w", "then": "t"}],
   "escape": [{"path": "internal/handler/a/gen_test.go", "kind": "test", "exists": true}]},
  {"id": "a.calc", "name": "Calc", "domain": "a", "source": {"path": "design/a/Calc.yaml"},
   "rules": [{"path": "design/a/Calc.yaml", "line": 12, "when": "w", "then": "t"}],
   "escape": [{"path": "internal/handler/a/calc_logic.go", "kind": "logic", "exists": true},
              {"path": "internal/handler/a/calc_logic_test.go", "kind": "test", "exists": true},
              {"path": "internal/handler/a/calc_extra.go", "kind": "handler", "exists": false}]},
  {"id": "a.sql", "name": "Sql", "domain": "a", "source": {"path": "specs/a/sql/spec.yaml"},
   "rules": [{"path": "specs/a/sql/spec.yaml", "line": 9, "when": "w", "then": "t"}],
   "escape": [{"path": "specs/a/sql/spec.yaml", "kind": "sql", "exists": true}]}],
  "hand_files": []}`

func TestParseAndFootprints(t *testing.T) {
	m, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	idx := m.Index()
	if idx[Key("a", "Gen")].Escaped() {
		t.Error("an API with only a test file is not escaped")
	}
	if !idx[Key("a", "Calc")].Escaped() {
		t.Error("an API with hand-written logic is escaped")
	}
	// Tests are not the implementation; SQL in the rule's own file is covered by the file itself.
	want := map[string][]string{"design/a/Calc.yaml:12": {"internal/handler/a/calc_logic.go"}, "specs/a/sql/spec.yaml:9": {}}
	if got := m.RuleFootprints(); !reflect.DeepEqual(got, want) {
		t.Errorf("footprints %v, want %v", got, want)
	}
}

func TestParseRejects(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"schema": "factory.api-manifest/v2", "apis": []}`, "this docsp reads"},
		{`{"schema": "contract/v1", "apis": []}`, "not an API manifest"},
		{`{"schema": "factory.api-manifest/v1", "apis": [{"id": "x"}]}`, "required"},
		{`{`, "factory manifest"},
	} {
		if _, err := Parse([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", tc.in, err, tc.want)
		}
	}
}
