// Package backlog imports a reviewed backlog of documentation defects (JSON Lines in the
// ground-truth format: repo, file, line, claim, actual, type, fix, evidence_paths) as
// findings/v1, so the shared ledger tracks the existing backlog with the same lifecycle as
// detected findings. The import is idempotent: the same item always gets the same finding id.
package backlog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/MIERAI/docs-platform/internal/findings"
)

// Version of the importer (recorded in collector.tools).
const Version = "0.1.0"

// Item is one reviewed backlog line.
type Item struct {
	Repo          string   `json:"repo"`
	File          string   `json:"file"`
	Line          *int     `json:"line"`
	Claim         string   `json:"claim"`
	Actual        string   `json:"actual"`
	Type          string   `json:"type"`
	Fix           string   `json:"fix"`
	EvidencePaths []string `json:"evidence_paths"`
}

// Parse decodes and checks a backlog. Unknown fields are ignored (the format may grow).
func Parse(b []byte) ([]Item, error) {
	var out []Item
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var it Item
		if err := json.Unmarshal(line, &it); err != nil {
			return nil, fmt.Errorf("backlog line %d: %w", n, err)
		}
		if err := it.check(); err != nil {
			return nil, fmt.Errorf("backlog line %d: %w", n, err)
		}
		out = append(out, it)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read backlog: %w", err)
	}
	return out, nil
}

func (it *Item) check() error {
	switch {
	case it.File == "" || strings.TrimSpace(it.Claim) == "":
		return errors.New("file and claim are required")
	case path.IsAbs(it.File) || strings.Contains("/"+it.File+"/", "/../"):
		return fmt.Errorf("file %q must be repository-relative", it.File)
	case it.Line != nil && *it.Line < 0:
		return errors.New("line must not be negative")
	}
	switch it.Type {
	case "wrong", "obsolete", "missing":
		return nil
	}
	return fmt.Errorf("type %q: want wrong, obsolete or missing", it.Type)
}

var severity = map[string]string{"wrong": "medium", "obsolete": "low", "missing": "low"}

// Build turns backlog items of one repository into a findings/v1 document (collector
// "docs-backlog"). Items of other repositories are skipped and counted.
func Build(p findings.Params, repoFull string, items []Item) (*findings.Document, int) {
	d := &findings.Document{
		SchemaVersion: findings.SchemaVersion,
		Collector:     findings.Collector{Name: "docs-backlog", Version: p.Version, Tools: map[string]string{"backlog": Version}},
		Run:           findings.Run{StartedAt: p.Started, FinishedAt: p.Finished, Instance: p.Instance},
		// An import inspects nothing: empty coverage, so the ledger never marks a finding fixed
		// because the backlog no longer lists it. Fixes are observed by checks, or by a human.
		Coverage: []findings.Coverage{},
		Findings: []findings.Finding{},
	}
	skipped := 0
	for _, it := range items {
		if repoFull != "" && it.Repo != "" && it.Repo != repoFull {
			skipped++
			continue
		}
		d.Findings = append(d.Findings, finding(p, it))
	}
	return d, skipped
}

func finding(p findings.Params, it Item) findings.Finding {
	h := sha256.Sum256([]byte(it.File + "\x00" + it.Type + "\x00" + it.Claim))
	subject := hex.EncodeToString(h[:])[:12]
	asset := fmt.Sprintf("doc:%s/%s#backlog:%s", p.Repo, it.File, subject)
	check := "docs.backlog." + it.Type
	line := 0
	if it.Line != nil {
		line = *it.Line
	}
	a := findings.DocsAttributes{
		FindingType: "backlog", RefKind: "claim", Subject: subject, Confidence: "high", DetectorLayer: "review",
		Suggestion: trunc(it.Fix, 2000), Claim: trunc(it.Claim, 500), Actual: trunc(it.Actual, 2000), GTType: it.Type,
		Locations: []findings.Location{{Path: it.File, Line: line, Role: "doc"}},
	}
	ev := []string{fmt.Sprintf("%s:%d", it.File, line)}
	for _, e := range it.EvidencePaths {
		a.Locations = append(a.Locations, findings.Location{Path: e, Role: "code"})
		ev = append(ev, e)
	}
	return findings.Finding{
		ID: findings.ID(findings.Layer, asset, check), Check: check, Layer: findings.Layer, Asset: asset,
		Title:    trunc(fmt.Sprintf("%s:%d: %s (%s)", it.File, line, it.Claim, it.Type), 300),
		Severity: severity[it.Type], Evidence: trunc(strings.Join(ev, ", "), 2000), Remediation: trunc(it.Fix, 2000),
		NeedsHuman: false, Owner: p.Owner, ObservedAt: p.Finished, Kind: "compliance",
		Attributes: map[string]any{"docs": a},
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
