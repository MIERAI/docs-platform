package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// The mutation set: deterministic code changes applied to scratch copies of two public
// repositories at their pinned revisions, each recording the documentation lines it makes
// wrong. It is a Go port of the prototype's Python mutation script (same mutations, same order, same
// output); `bench mutate` regenerates bench/data/mutations.json byte for byte.
//
// Semantics kept from the prototype: every mutation is applied on top of the previous ones
// (nothing is committed); the affected lines are the lines of the audited documents that
// contain one of the mutation's tokens, read before the mutation is applied; a "*" replace
// rewrites every tracked non-Markdown file (as listed by git ls-files, i.e. at its pinned
// path) that is still a regular file, is valid UTF-8 and contains the old text, writing it
// back with "\n" line ends.

// mutation is one code change.
type mutation struct {
	repo, id, op, a string
	b               any // rename: new path; replace: [old, new]; delete: nil
	tokens          []string
}

// mutAudit are the documents whose lines the mutations are scored on.
var mutAudit = map[string][]string{
	"gitops-manifests": {"README.md", "experiments/README.md"},
	"api-factory":      {"README.md", "runtime/README.md"},
}

func rn(repo, id, a, b string, tokens ...string) mutation {
	return mutation{repo: repo, id: id, op: "rename", a: a, b: b, tokens: tokens}
}

func del(repo, id, a string, tokens ...string) mutation {
	return mutation{repo: repo, id: id, op: "delete", a: a, tokens: tokens}
}

func rep(repo, id, a, old, nw string, tokens ...string) mutation {
	return mutation{repo: repo, id: id, op: "replace", a: a, b: []string{old, nw}, tokens: tokens}
}

const (
	mGM = "gitops-manifests"
	mAF = "api-factory"
)

var mutations = []mutation{
	rn(mGM, "G1", "scripts/fork.sh", "scripts/fork-repo.sh", "scripts/fork.sh"),
	rn(mGM, "G2", "scripts/promote.sh", "scripts/promote-version.sh", "scripts/promote.sh"),
	del(mGM, "G3", "scripts/render.py", "scripts/render.py"),
	rn(mGM, "G4", "scripts/validate.sh", "scripts/check-all.sh", "scripts/validate.sh"),
	rn(mGM, "G5", "scripts/alert-report.py", "scripts/alerts-review.py", "scripts/alert-report.py"),
	rn(mGM, "G6", "scripts/set-image.sh", "scripts/update-tag.sh", "set-image.sh"),
	rn(mGM, "G7", "reports/alerts", "reports/alerting", "reports/alerts/"),
	rep(mGM, "G8", "platform/delivery-board/deployment.yaml", "nodePort: 30090", "nodePort: 30095", "30090"),
	rep(mGM, "G9", "api-factory/base/smoke-job.yaml", "PostSync", "Sync", "PostSync"),
	rn(mGM, "G10", "sample-app", "reference-app", "sample-app/base"),
	rn(mGM, "G12", "experiments/api-errors.yaml", "experiments/api-http-errors.yaml", "experiments/api-errors.yaml"),
	del(mGM, "G13", "experiments/sample-app-down.yaml", "sample-app-down"),
	rn(mGM, "G14", "scripts/experiment.py", "scripts/run-experiment.py", "scripts/experiment.py"),
	rn(mGM, "G15", "scripts/test_alert_report.py", "scripts/alert_report_test.py", "scripts/test_alert_report.py"),
	rn(mGM, "G16", "scripts/test_scorecard.py", "tests/test_scorecard.py", "scripts/test_scorecard.py"),
	rn(mGM, "G17", "scripts/scorecard.py", "scripts/reliability.py", "scripts/scorecard.py"),
	rn(mGM, "G18", "scripts/experiment-regression.py", "scripts/regression.py", "scripts/experiment-regression.py"),
	rep(mGM, "G19", "platform/experiments/cronjob.yaml", `"0 9 * * *"`, `"0 6 * * *"`, "09:00 JST", "daily regression (09:00)"),
	rep(mGM, "G20", "*", "experiment-results", "experiment-outcomes", "experiment-results"),
	rep(mGM, "G21", "*", "experiment-exporter", "slo-experiment-exporter", "experiment-exporter"),
	rep(mGM, "G22", "*", "slo-experiments", "experiments-ns", "slo-experiments"),
	rn(mAF, "A1", "scripts/ci/spec-review.sh", "scripts/ci/review-spec.sh", "scripts/ci/spec-review.sh"),
	rn(mAF, "A2", "scripts/ci/generate.sh", "scripts/ci/gen.sh", "scripts/ci/generate.sh"),
	del(mAF, "A3", "scripts/ci/margin.sh", "scripts/ci/margin.sh"),
	rn(mAF, "A4", "scripts/ci/deploy.sh", "scripts/ci/deploy-run.sh", "scripts/ci/deploy.sh"),
	rn(mAF, "A5", "factory/review/fingerprint.go", "factory/review/fp.go", "fingerprint.go"),
	rn(mAF, "A6", "scripts/ci/review-key.sh", "scripts/ci/review-cache-key.sh", "scripts/ci/review-key.sh"),
	rn(mAF, "A7", "scripts/projects.sh", "scripts/ci/projects.sh", "scripts/projects.sh"),
	rn(mAF, "A8", "Dockerfile.schema", "Dockerfile.migrate", "Dockerfile.schema"),
	rn(mAF, "A9", "scripts/ci/sim.sh", "scripts/ci/simulate.sh", "scripts/ci/sim.sh"),
	rep(mAF, "A10", "*", "AUTH_MODE", "AUTH_KIND", "AUTH_MODE"),
	rep(mAF, "A11", "*", "AUTH_TOKEN_SHA256", "AUTH_TOKEN_HASH", "AUTH_TOKEN_SHA256"),
	rn(mAF, "A12", "scripts/release.sh", "scripts/ci/release.sh", "scripts/release.sh"),
	rn(mAF, "A13", ".github/workflows/runtime.yml", ".github/workflows/runtime-ci.yml", "workflows/runtime.yml", "`runtime.yml`"),
	rn(mAF, "A14", "factory/proj", "factory/project", "factory/proj`", "factory/proj/"),
	rn(mAF, "A15", "examples/storefront-v2/GAPS.md", "examples/storefront-v2/KNOWN-GAPS.md", "GAPS.md"),
	rn(mAF, "A16", "factory/acceptgen", "factory/acceptance", "factory/acceptgen"),
	rn(mAF, "A17", "factory/guard", "factory/gate", "factory/guard"),
	rn(mAF, "A18", "runtime/paging", "runtime/pagination", "runtime/paging", "`paging`"),
	rn(mAF, "A19", "runtime/clock", "runtime/timesource", "runtime/clock", "`clock`"),
	rn(mAF, "A20", "runtime/conv", "runtime/convert", "`conv`"),
	del(mAF, "A21", "runtime/testkit", "`testkit`"),
	rn(mAF, "A22", "runtime/broker", "runtime/amqp", "`broker`"),
	rn(mAF, "A23", "runtime/tracing/off_test.go", "runtime/tracing/disabled_test.go", "off_test.go"),
	rep(mAF, "A24", "*", "SHUTDOWN_TIMEOUT_MS", "SHUTDOWN_GRACE_MS", "SHUTDOWN_TIMEOUT_MS"),
	rep(mAF, "A25", "*", "RUNTIME_TEST_AMQP", "RUNTIME_TEST_RABBITMQ", "RUNTIME_TEST_AMQP"),
	rn(mAF, "A26", "runtime/outbox/perf_gate_test.go", "runtime/outbox/gate_test.go", "perf_gate_test.go"),
}

// mutRecord is one entry of mutations.json (field order as the prototype wrote it).
type mutRecord struct {
	Repo     string   `json:"repo"`
	ID       string   `json:"id"`
	Op       string   `json:"op"`
	A        string   `json:"a"`
	B        any      `json:"b"`
	Tokens   []string `json:"tokens"`
	Affected [][2]any `json:"affected"`
}

// runMutate is `bench mutate`: copies of the pinned public clones under -work are mutated in
// <out-dir>/<repo> and the mutation set is written to -out.
func runMutate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	file := fs.String("manifest", "bench/data/manifest.json", "benchmark manifest (pinned revisions)")
	work := fs.String("work", "", "directory holding the pinned clones (bench fetch)")
	outDir := fs.String("out-dir", "", "where the mutated copies are made (default <work>/mut); replaced if present")
	out := fs.String("out", "", "write the mutation set (JSON) here (default stdout)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("mutate flags: %w", err)
	}
	if *work == "" {
		return errors.New("mutate: -work is required")
	}
	m, err := LoadManifest(*file)
	if err != nil {
		return err
	}
	if *outDir == "" {
		*outDir = filepath.Join(*work, "mut")
	}
	for repo := range mutAudit {
		rev, ok := m.Pin(repo)
		if !ok {
			return fmt.Errorf("mutate: %s is not in the manifest", repo)
		}
		if err := copyAt(ctx, filepath.Join(*work, repo), filepath.Join(*outDir, repo), rev); err != nil {
			return err
		}
	}
	recs := make([]mutRecord, 0, len(mutations))
	for _, mu := range mutations {
		r, err := applyMutation(ctx, filepath.Join(*outDir, mu.repo), mu)
		if err != nil {
			return fmt.Errorf("mutate %s: %w", mu.id, err)
		}
		recs = append(recs, r)
	}
	b, err := encodeMutations(recs)
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = os.Stdout.Write(b)
		return err //nolint:wrapcheck // stdout
	}
	return os.WriteFile(*out, b, 0o600) //nolint:wrapcheck // operator-named output
}

// copyAt makes dst a fresh local clone of src at rev (the source clone is never changed).
func copyAt(ctx context.Context, src, dst, rev string) error {
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		return fmt.Errorf("mutate: no clone at %s (run bench fetch first)", src)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("mutate: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("mutate: %w", err)
	}
	if err := git(ctx, "", "clone", "--quiet", "--no-hardlinks", "--no-checkout", src, dst); err != nil {
		return err
	}
	return git(ctx, dst, "checkout", "--quiet", "--detach", rev)
}

func applyMutation(ctx context.Context, root string, mu mutation) (mutRecord, error) {
	rec := mutRecord{Repo: mu.repo, ID: mu.id, Op: mu.op, A: mu.a, B: mu.b, Tokens: mu.tokens, Affected: [][2]any{}}
	ls, err := gitOut(ctx, root, "ls-files")
	if err != nil {
		return rec, err
	}
	files := strings.Fields(ls)
	for _, d := range mutAudit[mu.repo] {
		b, err := os.ReadFile(filepath.Join(root, d))
		if err != nil {
			return rec, fmt.Errorf("audited doc: %w", err)
		}
		for i, l := range strings.Split(universalNewlines(string(b)), "\n") {
			for _, t := range mu.tokens {
				if strings.Contains(l, t) {
					rec.Affected = append(rec.Affected, [2]any{d, i + 1})
					break
				}
			}
		}
	}
	return rec, mutateTree(root, mu, files)
}

func mutateTree(root string, mu mutation, files []string) error {
	p := filepath.Join(root, filepath.FromSlash(mu.a))
	switch mu.op {
	case "rename":
		dst := filepath.Join(root, filepath.FromSlash(mu.b.(string)))
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("rename source: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return fmt.Errorf("rename: %w", err)
		}
		return os.Rename(p, dst) //nolint:wrapcheck // descriptive
	case "delete":
		return os.RemoveAll(p) //nolint:wrapcheck // descriptive
	case "replace":
		return replaceAll(root, mu, files)
	}
	return fmt.Errorf("unknown op %q", mu.op)
}

// replaceAll applies a replace mutation to its file, or with "*" to every tracked
// non-Markdown file that is still a regular file.
func replaceAll(root string, mu mutation, files []string) error {
	pair := mu.b.([]string)
	targets := []string{mu.a}
	if mu.a == "*" {
		targets = nil
		for _, f := range files {
			if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil && st.Mode().IsRegular() && !strings.HasSuffix(f, ".md") {
				targets = append(targets, f)
			}
		}
	}
	n := 0
	for _, f := range targets {
		changed, err := replaceIn(filepath.Join(root, filepath.FromSlash(f)), pair[0], pair[1])
		if err != nil && mu.a != "*" {
			return err
		}
		if changed {
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("replace %q: no file contains it", pair[0])
	}
	return nil
}

// replaceIn rewrites a file with every old replaced by nw (Python text mode: undecodable
// files are skipped, line ends are written as "\n").
func replaceIn(p, old, nw string) (bool, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return false, fmt.Errorf("replace: %w", err)
	}
	if !utf8.Valid(b) {
		return false, nil
	}
	s := universalNewlines(string(b))
	if !strings.Contains(s, old) {
		return false, nil
	}
	return true, os.WriteFile(p, []byte(strings.ReplaceAll(s, old, nw)), 0o600) //nolint:gosec,wrapcheck // scratch copy
}

func universalNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// encodeMutations writes the set as the prototype did: json.dump(indent=1,
// ensure_ascii=False), no trailing newline.
func encodeMutations(recs []mutRecord) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(recs); err != nil {
		return nil, fmt.Errorf("encode mutations: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// mutationStats counts mutations, affected lines and distinct affected lines.
func mutationStats(b []byte) (int, int, int, error) {
	var recs []struct {
		Repo     string  `json:"repo"`
		Affected [][]any `json:"affected"`
	}
	if err := json.Unmarshal(b, &recs); err != nil {
		return 0, 0, 0, fmt.Errorf("mutations: %w", err)
	}
	aff, uniq := 0, map[string]bool{}
	for _, r := range recs {
		aff += len(r.Affected)
		for _, a := range r.Affected {
			uniq[fmt.Sprint(r.Repo, a)] = true
		}
	}
	return len(recs), aff, len(uniq), nil
}
