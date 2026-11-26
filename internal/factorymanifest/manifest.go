// Package factorymanifest reads the API manifest an API factory exports
// (factory.api-manifest/v1, `factory export`): per API its human-written source, the
// locations of its business rules and examples, the files generated from it and its escape
// layer (hand-written logic, handlers, SQL).
//
// The factory keeps generated code in step with the source by its own gates, so docs-platform
// does not re-derive that code: it uses the manifest to know which APIs the generator
// guarantees and which rules sit next to hand-written code (where the source can go stale).
package factorymanifest

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// SchemaPrefix is the manifest family this package reads; v1 is the only major version.
const SchemaPrefix = "factory.api-manifest/"

// Schema is the version read.
const Schema = SchemaPrefix + "v1"

// Manifest is one project's manifest.
type Manifest struct {
	Schema    string   `json:"schema"`
	Adapter   string   `json:"adapter"`
	Project   string   `json:"project"`
	APIs      []API    `json:"apis"`
	HandFiles []string `json:"hand_files"`
}

// API is one RPC.
type API struct {
	ID         string    `json:"id"`
	RPC        string    `json:"rpc"`
	Name       string    `json:"name"`
	Domain     string    `json:"domain"`
	Kind       string    `json:"kind"`
	Source     Loc       `json:"source"`
	SourceKind string    `json:"source_kind"`
	Spec       string    `json:"spec"`
	Rules      []Rule    `json:"rules"`
	Examples   []Example `json:"examples"`
	Generated  []string  `json:"generated"`
	Escape     []Escape  `json:"escape"`
	Acceptance string    `json:"acceptance,omitempty"`
}

// Loc is a project-relative position (line 0 = the whole file).
type Loc struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// Rule is a business rule of the source.
type Rule struct {
	Loc
	When string `json:"when"`
	Then string `json:"then"`
}

// Example is an example the factory executes (acceptance case or logic example).
type Example struct {
	Loc
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Escape is a file of the escape layer.
type Escape struct {
	Loc
	Kind   string `json:"kind"` // logic | handler | test | sql
	Exists bool   `json:"exists"`
}

// Load reads and validates a manifest file.
func Load(file string) (*Manifest, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("factory manifest: %w", err)
	}
	return Parse(b)
}

// Parse validates a manifest: a known schema and APIs with an id, a name and a source.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("factory manifest: %w", err)
	}
	if m.Schema != Schema {
		if strings.HasPrefix(m.Schema, SchemaPrefix) {
			return nil, fmt.Errorf("factory manifest: schema %q: this docsp reads %s", m.Schema, Schema)
		}
		return nil, fmt.Errorf("factory manifest: schema %q is not an API manifest (want %s)", m.Schema, Schema)
	}
	for i, a := range m.APIs {
		if a.ID == "" || a.Name == "" || a.Source.Path == "" {
			return nil, fmt.Errorf("factory manifest: api %d: id, name and source are required", i)
		}
	}
	return &m, nil
}

// Escaped reports whether the API has hand-written code (an existing escape file other than a test).
func (a API) Escaped() bool {
	for _, e := range a.Escape {
		if e.Exists && e.Kind != "test" {
			return true
		}
	}
	return false
}

// Key is how an API is matched with a contract operation: its domain and name.
func Key(domain, name string) string { return domain + "/" + name }

// Index maps Key(domain, name) to the API.
func (m *Manifest) Index() map[string]API {
	out := make(map[string]API, len(m.APIs))
	for _, a := range m.APIs {
		out[Key(a.Domain, a.Name)] = a
	}
	return out
}

// RuleFootprints maps each business rule ("path:line") of an API with hand-written code to
// that code: when one of those files changes, the rule may no longer hold. Rules of
// generator-guaranteed APIs are left out (their behaviour follows the source).
func (m *Manifest) RuleFootprints() map[string][]string {
	out := map[string][]string{}
	for _, a := range m.APIs {
		if !a.Escaped() {
			continue
		}
		var files []string
		for _, e := range a.Escape {
			if e.Exists {
				files = append(files, e.Path)
			}
		}
		for _, r := range a.Rules {
			k := fmt.Sprintf("%s:%d", r.Path, r.Line)
			out[k] = append(out[k], files...)
		}
	}
	return out
}
