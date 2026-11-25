package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GTItem is one ground-truth line: a human-confirmed documentation defect.
type GTItem struct {
	Repo          string   `json:"repo"`
	File          string   `json:"file"`
	Line          *int     `json:"line"`
	Claim         string   `json:"claim"`
	Actual        string   `json:"actual"`
	Type          string   `json:"type"`
	Fix           string   `json:"fix"`
	EvidencePaths []string `json:"evidence_paths"`
	// Layer is optional; when absent it is estimated by classify().
	Layer string `json:"layer,omitempty"`
	// derived
	layerEstimated bool
}

func loadGT(dir string) (map[string][]GTItem, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}
	sort.Strings(files)
	out := map[string][]GTItem{}
	for _, f := range files {
		items, err := readJSONL(f)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Layer == "" {
				it.Layer, it.layerEstimated = classify(it), true
			}
			out[it.Repo] = append(out[it.Repo], it)
		}
	}
	return out, nil
}

func readJSONL(p string) ([]GTItem, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	var items []GTItem
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var it GTItem
		if err := json.Unmarshal(sc.Bytes(), &it); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", p, n, err)
		}
		items = append(items, it)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return items, nil
}

var (
	reNotExist = regexp.MustCompile(`(?i)does not exist|doesn't exist|not in this repository|is not in the repo|不存在|文件名是|no such file|unrelated project|private notes|retired`)
	reIdent    = regexp.MustCompile(`\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b|(?:^|\s)--[a-z][a-z0-9-]+|\b[a-z][a-z0-9]*_[a-z0-9_]*_(?:total|seconds|bytes|count)\b`)
)

// classify estimates which detector layer could find a ground-truth item. "reference" items
// are reachable by layer ③ in principle (a named identifier or path that does not exist, or
// an undocumented identifier); everything else needs semantic understanding (layer ④).
func classify(g GTItem) string {
	all := g.Claim + "\n" + g.Actual + "\n" + g.Fix
	if reNotExist.MatchString(all) {
		return "reference"
	}
	if g.Type == "missing" && reIdent.MatchString(g.Actual+" "+g.Fix) {
		return "reference"
	}
	return "semantic"
}
