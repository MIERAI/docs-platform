// Package yamlkeys flattens YAML documents into dotted key paths. It walks the node tree
// (aliases are not followed), bounds depth and node count, and never panics on bad input.
package yamlkeys

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// Limits protect against hostile or pathological input.
const (
	MaxDepth = 32
	MaxNodes = 200000
)

// Key is one dotted path. Sequence items contribute no segment: a.b[0].c is "a.b.c".
type Key struct {
	Path  string
	Leaf  string
	Line  int
	Depth int
	// Scalar is the value when it is a scalar ("" otherwise).
	Scalar string
}

// Doc is one YAML document of a stream.
type Doc struct {
	Root *yaml.Node
	Keys []Key
}

// Parse decodes every document of a YAML stream.
func Parse(b []byte) (docs []Doc, err error) {
	defer func() {
		if r := recover(); r != nil { // the decoder must never take the process down
			docs, err = nil, fmt.Errorf("yaml decoder panic: %v", r)
		}
	}()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
			return docs, fmt.Errorf("parse yaml: %w", err)
		}
		w := walker{}
		if len(n.Content) > 0 {
			w.walk(n.Content[0], "", 0)
		}
		if w.overflow {
			return docs, errors.New("yaml document too large or too deep")
		}
		docs = append(docs, Doc{Root: contentRoot(&n), Keys: w.keys})
	}
}

func contentRoot(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

type walker struct {
	keys     []Key
	nodes    int
	overflow bool
}

func (w *walker) walk(n *yaml.Node, prefix string, depth int) {
	w.nodes++
	if depth > MaxDepth || w.nodes > MaxNodes {
		w.overflow = true
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.Value == "<<" {
				continue
			}
			p := join(prefix, k.Value)
			key := Key{Path: p, Leaf: k.Value, Line: k.Line, Depth: depth}
			if v.Kind == yaml.ScalarNode {
				key.Scalar = v.Value
			}
			w.keys = append(w.keys, key)
			w.walk(v, p, depth+1)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			w.walk(c, prefix, depth+1)
		}
	default:
	}
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// Get returns the child of a mapping node by key, or nil.
func Get(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// Path follows a sequence of mapping keys.
func Path(n *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		n = Get(n, k)
	}
	return n
}

// MapKeys returns the scalar keys of a mapping node with their lines.
func MapKeys(n *yaml.Node) []Key {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]Key, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, Key{Path: n.Content[i].Value, Leaf: n.Content[i].Value, Line: n.Content[i].Line})
	}
	return out
}
