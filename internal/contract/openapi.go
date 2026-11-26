package contract

import (
	"context"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// GraphQLVersion is the version of the GraphQL adapters.
const GraphQLVersion = "1.0.0"

var (
	reGQLStart = regexp.MustCompile(`^\s*(query|mutation|subscription|fragment)\b|^\s*\{`)
	reGQLFence = regexp.MustCompile("(?s)```(?:graphql|gql)\\s*\\n(.*?)```")
)

// OpenAPIGraphQL extracts the design surface of OpenAPI documents (YAML or JSON) whose paths
// embed GraphQL: every "query" string of an example and every ```graphql block of a
// description is parsed; each root field is an operation, its arguments typed from the
// variable definitions (and, for an input object, its properties from components.schemas),
// its outputs the selected fields. The group is the document's directory.
func OpenAPIGraphQL(ctx context.Context, src *Source) (*Surface, error) {
	s := &Surface{Side: SideDesign, Adapter: AdapterInfo{Name: "openapi-graphql", Version: GraphQLVersion}, Source: src.Info()}
	for _, f := range src.Files() {
		ext := path.Ext(f)
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			continue
		}
		b, err := src.Read(ctx, f)
		if err != nil {
			s.Gaps = append(s.Gaps, f)
			continue
		}
		ops, ok := openAPIOps(f, b)
		if !ok {
			s.Gaps = append(s.Gaps, f)
		}
		s.Operations = append(s.Operations, ops...)
	}
	s.Operations = mergeOps(s.Operations)
	s.Normalize()
	return s, nil
}

// openAPIOps reads one document; ok is false when it does not parse.
func openAPIOps(f string, b []byte) ([]Operation, bool) {
	var root map[string]any
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, false
	}
	if root["openapi"] == nil && root["swagger"] == nil {
		return nil, true
	}
	schemas := mapOf(mapOf(root, "components"), "schemas")
	var docs []string
	collectGraphQL(root, &docs, 0)
	text := string(b)
	ok := true
	var ops []Operation
	for _, d := range docs {
		gops, err := parseGraphQL(d)
		if err != nil {
			ok = false
		}
		for _, g := range gops {
			op := Operation{Name: g.Name, Kind: g.Kind, Group: path.Base(path.Dir(f)), Path: f, Line: nameLine(text, g.Name), Files: []string{f}, Outputs: g.Fields}
			for _, a := range g.Args {
				a.Fields = schemaFields(schemas, strings.Trim(a.Type, "[]!"), 0)
				if len(a.Fields) > 0 || schemas[strings.Trim(a.Type, "[]!")] != nil {
					a.Type = objType(a.Type)
				}
				op.Inputs = append(op.Inputs, a)
			}
			ops = append(ops, op)
		}
	}
	return ops, ok
}

// objType marks a named input object type: "[Dto!]" → "[obj:Dto]".
func objType(t string) string {
	name := strings.Trim(t, "[]!")
	return strings.Replace(strings.ReplaceAll(t, "!", ""), name, "obj:"+name, 1)
}

// collectGraphQL walks a decoded document for GraphQL text.
func collectGraphQL(v any, out *[]string, depth int) {
	if depth > 64 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			if s, ok := x[k].(string); ok {
				if k == "query" && reGQLStart.MatchString(s) && strings.Contains(s, "{") {
					*out = append(*out, s)
				}
				for _, m := range reGQLFence.FindAllStringSubmatch(s, -1) {
					*out = append(*out, m[1])
				}
				continue
			}
			collectGraphQL(x[k], out, depth+1)
		}
	case []any:
		for _, e := range x {
			collectGraphQL(e, out, depth+1)
		}
	}
}

// schemaFields are the properties of a components.schemas object (nested objects to a
// small depth).
func schemaFields(schemas map[string]any, name string, depth int) []Field {
	sch := asMap(schemas[name])
	if sch == nil || depth > 3 {
		return nil
	}
	required := map[string]bool{}
	for _, r := range listOf(sch, "required") {
		required[scalar(r)] = true
	}
	var fs []Field
	for _, pn := range sortedKeys(mapOf(sch, "properties")) {
		pm := asMap(mapOf(sch, "properties")[pn])
		f := Field{Name: pn, Type: openAPIType(pm)}
		switch {
		case required[pn]:
			f.Presence = Required
		case pm["nullable"] == true:
			f.Presence = Optional
		}
		if ref := refName(pm); ref != "" {
			f.Fields = schemaFields(schemas, ref, depth+1)
		}
		fs = append(fs, f)
	}
	return fs
}

func refName(pm map[string]any) string {
	ref := str(pm, "$ref")
	if ref == "" {
		ref = str(mapOf(pm, "items"), "$ref")
	}
	if i := strings.LastIndexByte(ref, '/'); i >= 0 {
		return ref[i+1:]
	}
	return ""
}

// openAPIType maps an OpenAPI schema to the GraphQL notation the Kotlin adapter uses; an
// integer or number without format stays generic ("integer", "number").
func openAPIType(pm map[string]any) string {
	if ref := str(pm, "$ref"); ref != "" {
		return "obj:" + ref[strings.LastIndexByte(ref, '/')+1:]
	}
	switch str(pm, "type") {
	case "integer":
		switch str(pm, "format") {
		case "int32":
			return "Int"
		case "int64":
			return "BigInteger"
		}
		return "integer"
	case "number":
		if f := str(pm, "format"); f == "float" || f == "double" {
			return "Float"
		}
		return "number"
	case "string":
		return "String"
	case "boolean":
		return "Boolean"
	case "array":
		return "[" + openAPIType(mapOf(pm, "items")) + "]"
	}
	return ""
}

// nameLine is the first line of text naming the operation as a field ("name(" or "name {").
func nameLine(text, name string) int {
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `\s*[({]`)
	if loc := re.FindStringIndex(text); loc != nil {
		return lineOf(text, loc[0])
	}
	return 0
}

// mergeOps joins operations of the same name and group (several examples of one query):
// arguments and selected fields are united.
func mergeOps(ops []Operation) []Operation {
	var out []Operation
	idx := map[string]int{}
	for _, op := range ops {
		k := op.Group + "\x00" + op.Name
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, op)
			continue
		}
		for _, a := range op.Inputs {
			if _, ok := field(out[i].Inputs, a.Name); !ok {
				out[i].Inputs = append(out[i].Inputs, a)
			}
		}
		for _, f := range op.Outputs {
			out[i].Outputs = mergeField(out[i].Outputs, f)
		}
	}
	return out
}
