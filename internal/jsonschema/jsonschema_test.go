package jsonschema

import (
	"strings"
	"testing"
)

const schema = `{
  "type": "object", "required": ["a"], "additionalProperties": false,
  "properties": {
    "a": {"type": "string", "pattern": "^x", "maxLength": 3},
    "b": {"type": "array", "items": {"$ref": "#/$defs/n"}},
    "c": {"enum": ["p", "q"]},
    "d": {"type": "object", "additionalProperties": {"type": "integer"}}
  },
  "$defs": {"n": {"type": "number"}}
}`

func TestValidate(t *testing.T) {
	s, err := Compile([]byte(schema))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		doc, wantErr string
	}{
		{`{"a":"xy"}`, ""},
		{`{"a":"xy","b":[1,2.5],"c":"p","d":{"k":3}}`, ""},
		{`{}`, "missing a"},
		{`{"a":"yy"}`, "does not match"},
		{`{"a":"xyzw"}`, "longer than"},
		{`{"a":"x","b":["s"]}`, "$.b[0]: expected number"},
		{`{"a":"x","c":"z"}`, "not in"},
		{`{"a":"x","d":{"k":1.5}}`, "expected integer"},
		{`{"a":"x","e":1}`, "unexpected property"},
		{`[`, "invalid JSON"},
	}
	for _, c := range cases {
		errs := s.ValidateJSON([]byte(c.doc))
		if c.wantErr == "" && len(errs) > 0 {
			t.Errorf("%s: unexpected %v", c.doc, errs)
		}
		if c.wantErr != "" && !strings.Contains(strings.Join(errs, "|"), c.wantErr) {
			t.Errorf("%s: errors %v, want %q", c.doc, errs, c.wantErr)
		}
	}
}

func TestBadRef(t *testing.T) {
	s, err := Compile([]byte(`{"$ref":"#/$defs/missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if errs := s.ValidateJSON([]byte(`1`)); len(errs) == 0 {
		t.Error("unresolvable $ref must be reported")
	}
}
