package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/factoryrules"
	"github.com/MIERAI/docs-platform/internal/factorytry"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/rulecover"
)

const defaultFactory = "go run ./factory/cmd/factory"

// factoryFlags are the flags the factory-loop commands share.
type factoryFlags struct {
	manifest, factory, instanceFile, repoName, out, format string
}

func (f *factoryFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.manifest, "factory-manifest", "", "the factory's API manifest (factory.api-manifest/v1) of the checked-out revision (required)")
	fs.StringVar(&f.factory, "factory", defaultFactory, "the factory command, run in the checkout (`factory try` proves or refutes every proposal)")
	fs.StringVar(&f.instanceFile, "instance-config", "", "instance configuration: ai.judge (and ai.draft), budget, classification (required)")
	fs.StringVar(&f.repoName, "repo-name", "", "logical repository name (default: directory name)")
	fs.StringVar(&f.out, "out", "", "write the JSON report here (mode 0600)")
	fs.StringVar(&f.format, "format", "text", "stdout format: text, json, markdown (factory-pr) or none")
}

func (f *factoryFlags) load(fs *flag.FlagSet) (*factorymanifest.Manifest, *instance.Instance, string, string, error) {
	if f.manifest == "" || f.instanceFile == "" {
		return nil, nil, "", "", errors.New("-factory-manifest and -instance-config are required")
	}
	m, err := factorymanifest.Load(f.manifest)
	if err != nil {
		return nil, nil, "", "", err //nolint:wrapcheck // already descriptive
	}
	in, err := instance.Load(f.instanceFile)
	if err != nil {
		return nil, nil, "", "", err //nolint:wrapcheck // already descriptive
	}
	root := rootArg(fs)
	name := f.repoName
	if name == "" {
		name = repoBase(root)
	}
	return m, in, root, name, nil
}

func (f *factoryFlags) runner(root string) *factorytry.Command {
	return &factorytry.Command{Argv: strings.Fields(f.factory), Dir: root}
}

// guards: the judge, and the drafter (ai.draft when configured, else the judge).
func (f *factoryFlags) guards() (*judge.Guard, *judge.Guard, error) {
	g, err := check.NewJudge(f.instanceFile, "judge", false)
	if err != nil || g == nil {
		return nil, nil, errors.Join(errors.New("needs ai.judge in the instance configuration"), err)
	}
	d, err := check.NewJudge(f.instanceFile, "draft", false)
	if err != nil {
		_ = g.Close()
		return nil, nil, err //nolint:wrapcheck // already descriptive
	}
	if d == nil {
		d = g
	}
	return g, d, nil
}

// cmdFactoryPR reviews a change from the design's side: the business rules with hand-written
// code it touches, judged at the head, with factory-verified design drafts for the rules
// the code contradicts; optionally posted as one PR comment that is updated in place.
func cmdFactoryPR(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("factory-pr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f factoryFlags
	f.register(fs)
	base := fs.String("base", "", "the change's base revision, e.g. origin/main (required; the checkout is the head)")
	noTry := fs.Bool("no-try", false, "do not run the factory: drafts stay unverified")
	commentOut := fs.String("comment-out", "", "write the PR comment (Markdown) here")
	post := fs.Int("post-pr", 0, "post the comment to this pull request with gh (updated in place; needs -github-repo)")
	ghRepo := fs.String("github-repo", "", "owner/name of the pull request's repository (for -post-pr)")
	fromReport := fs.String("from-report", "", "render (and with -post-pr post) this factory-pr/v1 report instead of reviewing: no model, no factory")
	feedbackOnly := fs.Bool("feedback-only", false, "only read the boxes ticked in the posted comment (-post-pr, -github-repo): JSON on stdout / -out, no review")
	cache := fs.String("drift-cache", agentic.DefaultCacheDir(), "drift lists cached by HEAD SHA (\"\" = none)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *feedbackOnly {
		return readFeedback(ctx, *ghRepo, *post, f.out, stdout)
	}
	if *fromReport != "" {
		return rerender(ctx, *fromReport, &f, *commentOut, *ghRepo, *post, stdout, stderr)
	}
	if *base == "" || (*post > 0 && *ghRepo == "") {
		return exitUsage, errors.New("-base is required; -post-pr needs -github-repo")
	}
	m, in, root, name, err := f.load(fs)
	if err != nil {
		return exitUsage, err
	}
	head, changed, err := factorypr.Changed(ctx, root, *base)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if len(factorypr.Touched(m, changed)) == 0 { // nothing to judge: no model, no factory run
		rep := &factorypr.Report{SchemaVersion: factorypr.SchemaVersion, Repo: name, Base: *base, Head: head, Rules: []factorypr.Rule{}, GitHub: *ghRepo}
		return emitPR(ctx, rep, &f, *commentOut, *ghRepo, *post, stdout, stderr)
	}
	rep, err := reviewPR(ctx, &f, m, in, root, name, *base, *cache, !*noTry)
	if err != nil {
		return exitUsage, err
	}
	rep.GitHub = *ghRepo
	return emitPR(ctx, rep, &f, *commentOut, *ghRepo, *post, stdout, stderr)
}

// rerender renders (and posts) a saved factory-pr/v1 report: no model, no factory.
func rerender(ctx context.Context, file string, f *factoryFlags, commentOut, ghRepo string, post int, stdout, stderr io.Writer) (int, error) {
	var rep factorypr.Report
	b, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(b, &rep)
	}
	if err != nil {
		return exitUsage, fmt.Errorf("from-report: %w", err)
	}
	if ghRepo != "" {
		rep.GitHub = ghRepo
	}
	return emitPR(ctx, &rep, f, commentOut, ghRepo, post, stdout, stderr)
}

// reviewPR sets up the judge, the drafter and the factory and reviews the change.
func reviewPR(ctx context.Context, f *factoryFlags, m *factorymanifest.Manifest, in *instance.Instance, root, name, base, cache string, try bool) (*factorypr.Report, error) {
	g, d, err := f.guards()
	if err != nil {
		return nil, err
	}
	defer closeGuards(g, d)
	b, err := ledger.NewBuilder(ctx, root, name, "HEAD")
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	b.Class, b.Model = in.Classification, g.Name()
	run := agentic.New(agentic.Options{
		Guard: g, Repo: name, Dir: root, Class: in.Classification, CacheDir: cache,
		Settings: agentic.FromInstance(in.AI.Judge.Agentic), Hint: factoryrules.ManifestHint(m),
	})
	if dl, err := run.Drift(ctx); err == nil {
		b.Drift = dl
	}
	o := factorypr.Options{Repo: name, Dir: root, Base: base, Manifest: m, Run: run, Builder: b, Draft: d}
	if try {
		o.Try = f.runner(root)
	}
	rep, err := factorypr.Review(ctx, o)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	rep.USD = g.Stats().CostUSD
	if d != g {
		rep.USD += d.Stats().CostUSD
	}
	return rep, nil
}

// emitPR writes the review: the comment file, the PR comment, then stdout / -out.
func emitPR(ctx context.Context, rep *factorypr.Report, f *factoryFlags, commentOut, ghRepo string, post int, stdout, stderr io.Writer) (int, error) {
	var id, body string
	if post > 0 { // what people ticked: read back, kept in the new version
		var err error
		if id, body, err = findComment(ctx, ghRepo, post, factorypr.Marker); err != nil {
			return exitUsage, err
		}
		rep.Feedback = factorypr.Ticked(body)
	}
	md := rep.MarkdownWith(factorypr.TickedKeys(rep.Feedback))
	if commentOut != "" {
		if err := os.WriteFile(commentOut, []byte(md), 0o600); err != nil {
			return exitUsage, fmt.Errorf("comment: %w", err)
		}
	}
	if post > 0 && id != "" && rep.Unchanged(body) {
		_, _ = fmt.Fprintln(stderr, "docsp factory-pr: the posted comment already says this: not updated")
	} else if post > 0 {
		if err := postComment(ctx, ghRepo, post, id, md, factorypr.NoRulesComment()); err != nil {
			return exitUsage, err
		}
	}
	if f.format == "markdown" {
		_, err := io.WriteString(stdout, md)
		return exitOK, err //nolint:wrapcheck // stdout write
	}
	c := consFlags{out: f.out, format: f.format}
	return exitOK, c.emitReport(rep, func(w io.Writer) {
		_, _ = fmt.Fprintf(w, "docsp factory-pr: %d rules touched, %d contradicted by the code; $%.4f\n", len(rep.Rules), rep.Problems(), rep.USD)
		for _, r := range rep.Rules {
			v := ""
			if r.Draft != nil {
				v = map[bool]string{true: " - draft verified by the factory", false: " - draft not verified: " + r.Draft.Problem}[r.Draft.Verified]
			}
			_, _ = fmt.Fprintf(w, "  %-20s %s:%d%s\n", r.Category, r.Path, r.Line, v)
		}
	}, stdout, stderr)
}

func closeGuards(g, d *judge.Guard) {
	_ = g.Close()
	if d != g {
		_ = d.Close()
	}
}

// findComment is the review's comment on a pull request (by factorypr.Marker): its id and
// body ("" when there is none yet).
func findComment(ctx context.Context, repo string, pr int, marker string) (string, string, error) {
	out, err := exec.CommandContext(ctx, "gh", "api", "--paginate", //nolint:gosec // fixed program, argv vector
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, pr), "--jq", `.[] | select(.body | contains("`+marker+`")) | {id, body}`).Output()
	if err != nil {
		return "", "", fmt.Errorf("post-pr: list comments: %w", err)
	}
	var last struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		if err := dec.Decode(&last); err != nil {
			return "", "", fmt.Errorf("post-pr: comments: %w", err)
		}
	}
	if last.ID == 0 {
		return "", "", nil
	}
	return strconv.FormatInt(last.ID, 10), last.Body, nil
}

// readFeedback prints the boxes ticked in the posted comment (no review, no model).
func readFeedback(ctx context.Context, repo string, pr int, out string, stdout io.Writer) (int, error) {
	if pr <= 0 || repo == "" {
		return exitUsage, errors.New("-feedback-only needs -post-pr and -github-repo")
	}
	id, body, err := findComment(ctx, repo, pr, factorypr.Marker)
	if err != nil {
		return exitUsage, err
	}
	b, _ := json.MarshalIndent(map[string]any{"pr": pr, "comment": id, "feedback": factorypr.Ticked(body)}, "", "  ")
	b = append(b, '\n')
	if out != "" {
		if err := os.WriteFile(out, b, 0o600); err != nil {
			return exitUsage, fmt.Errorf("feedback: %w", err)
		}
	}
	_, err = stdout.Write(b)
	return exitOK, err //nolint:wrapcheck // stdout write
}

// postComment creates (id "") or updates the review's comment on a pull request with the gh
// CLI. An empty body updates an existing comment to say nothing is touched any more and
// creates none.
func postComment(ctx context.Context, repo string, pr int, id, body, gone string) error {
	if body == "" {
		if id == "" {
			return nil
		}
		body = gone
	}
	tmp, err := os.CreateTemp("", "docsp-comment-*.json")
	if err != nil {
		return fmt.Errorf("post-pr: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := json.NewEncoder(tmp).Encode(map[string]string{"body": body}); err != nil {
		return fmt.Errorf("post-pr: %w", err)
	}
	_ = tmp.Close()
	args := []string{"api", "--method", "POST", fmt.Sprintf("repos/%s/issues/%d/comments", repo, pr), "--input", tmp.Name()}
	if id != "" {
		args = []string{"api", "--method", "PATCH", fmt.Sprintf("repos/%s/issues/comments/%s", repo, id), "--input", tmp.Name()}
	}
	if b, err := exec.CommandContext(ctx, "gh", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed program, argv vector
		msg := strings.TrimSpace(string(b))
		return fmt.Errorf("post-pr: %w: %s", err, msg[:min(200, len(msg))])
	}
	return nil
}

// apiList is a repeatable -api flag.
type apiList []string

func (a *apiList) String() string     { return strings.Join(*a, ",") }
func (a *apiList) Set(v string) error { *a = append(*a, v); return nil }

// cmdFactoryCoverage measures the rule executable coverage of an API factory's escape layer
// and proposes factory-verified examples for the rules no example pins down.
func cmdFactoryCoverage(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("factory-coverage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f factoryFlags
	f.register(fs)
	prevFile := fs.String("previous", "", "the last report: rules whose text, design and code are unchanged are reused (default: -out)")
	propose := fs.Bool("propose", true, "draft examples for uncovered rules (each verified by the factory)")
	workers := fs.Int("workers", 2, "factory runs in parallel")
	var only apiList
	fs.Var(&only, "api", "limit to this API id (repeatable)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	m, _, root, name, err := f.load(fs)
	if err != nil {
		return exitUsage, err
	}
	if *prevFile == "" {
		*prevFile = f.out
	}
	prev, err := rulecover.Load(*prevFile)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	g, d, err := f.guards()
	if err != nil {
		return exitUsage, err
	}
	defer closeGuards(g, d)
	head, err := ledger.Resolve(ctx, root, "HEAD")
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	rep, err := rulecover.Measure(ctx, rulecover.Options{
		Repo: name, Dir: root, Ref: head, Manifest: m, Guard: g,
		Try: f.runner(root), Previous: prev, Propose: *propose, Workers: *workers, Only: only,
	})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	c := consFlags{out: f.out, format: f.format}
	return exitOK, c.emitReport(rep, func(w io.Writer) { writeCoverage(w, rep) }, stdout, stderr)
}

func writeCoverage(w io.Writer, rep *rulecover.Report) {
	s := rep.Summary
	cov := "not measured"
	if s.Coverage != nil {
		cov = strconv.FormatFloat(100**s.Coverage, 'f', 0, 64) + "%"
	}
	_, _ = fmt.Fprintf(w, "docsp factory-coverage: %s @%.12s - rule executable coverage %s (%d of %d measured rules pinned down by a design example; %d rules, %d reused); %d verified example proposals; $%.4f\n",
		rep.Repo, rep.Ref, cov, s.Covered, s.Measured, s.Rules, s.Reused, s.Proposals, rep.USD)
	rules := append([]rulecover.Rule{}, rep.Rules...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Status < rules[j].Status })
	for _, r := range rules {
		detail := ""
		switch {
		case r.Status == rulecover.Covered:
			detail = "caught by " + strings.Join(r.CaughtBy[:min(2, len(r.CaughtBy))], "; ")
		case r.Proposal != nil && r.Proposal.Verified:
			detail = "verified example proposed"
		case r.Proposal != nil:
			detail = "no verified example: " + r.Proposal.Problem
		case r.Problem != "":
			detail = r.Problem
		}
		_, _ = fmt.Fprintf(w, "  %-16s %s:%d %s\n", r.Status, r.Path, r.Line, cutRunes(detail, 140))
	}
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
