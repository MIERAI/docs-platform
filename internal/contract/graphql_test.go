package contract

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const gqlDoc = `
# a comment
query Shelf($input: ShelfInput!, $ids: [Int!], $tag: String = "x") {
  shelves(input: $input, ids: $ids, limit: 10) {
    code
    label: name
    rows { position, ...RowParts }
    ... on SpecialShelf { promo }
  }
  other: clock { now }
}
fragment RowParts on Row { width depth }
mutation { renameShelf(code: "A", name: $tag) { ok } }
`

func TestParseGraphQL(t *testing.T) {
	ops, err := parseGraphQL(gqlDoc)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range ops {
		names = append(names, o.Kind+":"+o.Name)
	}
	if strings.Join(names, ",") != "query:shelves,query:clock,mutation:renameShelf" {
		t.Fatalf("ops %v", names)
	}
	wantArgs := []Field{
		{Name: "input", Type: "ShelfInput", Presence: Required},
		{Name: "ids", Type: "[Int!]", Presence: Optional},
		{Name: "limit", Type: "Int", Presence: Optional},
	}
	if !reflect.DeepEqual(ops[0].Args, wantArgs) {
		t.Errorf("args %+v", ops[0].Args)
	}
	got := fieldPaths(ops[0].Fields, "")
	want := "code,name,rows,rows.position,rows.width,rows.depth,promo"
	if strings.Join(got, ",") != want {
		t.Errorf("fields %v", got)
	}
}

func fieldPaths(fs []Field, prefix string) []string {
	var out []string
	for _, f := range fs {
		out = append(out, prefix+f.Name)
		out = append(out, fieldPaths(f.Fields, prefix+f.Name+".")...)
	}
	return out
}

func TestParseGraphQLBroken(t *testing.T) {
	ops, err := parseGraphQL("query { a { b } } query { c(")
	if err == nil && len(ops) != 2 {
		t.Errorf("ops %+v err %v", ops, err)
	}
	if len(ops) == 0 || ops[0].Name != "a" {
		t.Errorf("the valid part is kept: %+v", ops)
	}
}

const openAPIDoc = `openapi: 3.0.0
info: { title: shelf service, version: v1 }
paths:
  /graphql:
    post:
      description: |
        Example:
        ` + "```graphql" + `
        query { shelfCount(zone: 3) }
        ` + "```" + `
      requestBody:
        content:
          application/json:
            examples:
              fetchShelf:
                value:
                  query: |
                    query FetchShelf($input: ShelfInput!) {
                      shelves(input: $input) { code name }
                    }
              fetchShelfAgain:
                value:
                  query: "query ($input: ShelfInput!) { shelves(input: $input) { depth } }"
components:
  schemas:
    ShelfInput:
      type: object
      required: [zone]
      properties:
        zone: { type: integer }
        codes: { type: array, nullable: true, items: { type: string } }
        size: { $ref: '#/components/schemas/Size' }
    Size:
      type: object
      properties:
        width: { type: number, format: double }
`

func TestOpenAPIGraphQL(t *testing.T) {
	ops, ok := openAPIOps("api/shelf/shelf.yaml", []byte(openAPIDoc))
	if !ok {
		t.Fatal("parse failed")
	}
	ops = mergeOps(ops)
	if len(ops) != 2 {
		t.Fatalf("ops %+v", ops)
	}
	var shelves Operation
	for _, o := range ops {
		if o.Name == "shelves" {
			shelves = o
		}
	}
	if shelves.Group != "shelf" || shelves.Line == 0 {
		t.Errorf("group %q line %d", shelves.Group, shelves.Line)
	}
	if got := strings.Join(fieldPaths(shelves.Outputs, ""), ","); got != "code,name,depth" {
		t.Errorf("merged outputs %v", got)
	}
	in := shelves.Inputs[0]
	if in.Type != "obj:ShelfInput" || in.Presence != Required {
		t.Errorf("input %+v", in)
	}
	want := []Field{
		{Name: "codes", Type: "[String]", Presence: Optional},
		{Name: "size", Type: "obj:Size", Fields: []Field{{Name: "width", Type: "Float"}}},
		{Name: "zone", Type: "integer", Presence: Required},
	}
	if !reflect.DeepEqual(in.Fields, want) {
		t.Errorf("input fields %+v", in.Fields)
	}
}

func TestOpenAPIIgnoresOtherYAML(t *testing.T) {
	ops, ok := openAPIOps("x.yaml", []byte("query: 'query { a }'\n"))
	if !ok || len(ops) != 0 {
		t.Errorf("a non-OpenAPI document is not a design: %+v", ops)
	}
	if _, ok := openAPIOps("x.yaml", []byte("openapi: [")); ok {
		t.Error("broken YAML must be a gap")
	}
}

func FuzzParseGraphQL(f *testing.F) {
	f.Add(gqlDoc)
	f.Add("query ($a: [[Int!]!]!) { x(a: $a, b: {c: [1, 2.5e3, \"s\"]}) @skip(if: true) { ...F } }")
	f.Add(`{ a(b: """block""") { c } }`)
	f.Add("fragment F on T { a ...F }  { ...F }")
	f.Fuzz(func(t *testing.T, s string) {
		ops, _ := parseGraphQL(s)
		for _, o := range ops {
			if o.Name == "" {
				t.Fatal("an operation without a name")
			}
		}
	})
}

func FuzzOpenAPIOps(f *testing.F) {
	f.Add([]byte(openAPIDoc))
	f.Add([]byte("openapi: 3\npaths: {q: {query: '{ a(b: $c) { d } }'}}"))
	f.Fuzz(func(_ *testing.T, b []byte) {
		ops, _ := openAPIOps("f.yaml", b)
		_ = mergeOps(ops)
	})
}

func TestOpenAPIGraphQLAdapter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "api/shelf/shelf.yaml", openAPIDoc)
	writeFile(t, dir, "api/shelf/README.md", "not a design")
	src, err := OpenSource(context.Background(), "specs", dir, "", []string{"api/**"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenAPIGraphQL(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Operations) != 2 || len(s.Gaps) != 0 || s.Adapter.Name != "openapi-graphql" {
		t.Errorf("surface %+v", s)
	}
}
