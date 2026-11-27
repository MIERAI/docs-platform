package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/draftverify"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/sandbox"
)

// draftSubs are the sub-commands of "docsp draft".
var draftSubs = map[string]command{"verify": cmdDraftVerify, "apply": cmdDraftApply}

// verifyFlags select the drafts: from the drafts log, or one whole-file draft.
type verifyFlags struct {
	ledger, log, claim, path, newFile, baseBlob, ref, repoName, out, format, image string
	noSandbox                                                                      bool
	timeout                                                                        time.Duration
}

func (f *verifyFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.ledger, "ledger", "", "claim ledger (docs-platform/ledger/v1): the evidence the changed claims are re-verified against")
	fs.StringVar(&f.log, "log", "", "drafts log (docsp draft -log): verify its proposed drafts")
	fs.StringVar(&f.claim, "claim", "", "only the draft of this claim id (with -log)")
	fs.StringVar(&f.path, "path", "", "a whole-file draft: the document's repository-relative path (with -new)")
	fs.StringVar(&f.newFile, "new", "", "a whole-file draft: the file holding the drafted document")
	fs.StringVar(&f.baseBlob, "base-blob", "", "a whole-file draft: the document's blob it was made against")
	fs.StringVar(&f.ref, "ref", "HEAD", "revision the ledger's evidence is checked against")
	fs.StringVar(&f.repoName, "repo-name", "", "logical repository name of the claims (default: directory name)")
	fs.StringVar(&f.out, "out", "", "write the JSON result here (mode 0600)")
	fs.StringVar(&f.format, "format", "text", "stdout format: text, json or none")
	fs.StringVar(&f.image, "image", sandbox.DefaultImage, "sandbox image (pin it by digest)")
	fs.BoolVar(&f.noSandbox, "no-sandbox", false, "do not run examples (the examples gate is skipped)")
	fs.DurationVar(&f.timeout, "timeout", sandbox.DefaultTimeout, "wall-clock limit of one example")
}

// verifyOutput is the command's JSON: one draft-verify/v1 result per draft.
type verifyOutput struct {
	SchemaVersion string                `json:"schema_version"`
	Schema        string                `json:"schema"`
	Results       []*draftverify.Result `json:"results"`
}

// cmdDraftVerify machine-verifies drafts (docs-platform/draft-verify/v1); exit code 1 when
// any is not verified.
func cmdDraftVerify(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("draft verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f verifyFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	ins, err := f.inputs(fs)
	if err != nil {
		return exitUsage, err
	}
	var sb sandbox.Runner
	if !f.noSandbox {
		if d, err := sandbox.Detect(ctx); err == nil {
			sb = d
		}
	}
	out := verifyOutput{SchemaVersion: draftverify.SchemaVersion, Schema: draftverify.Schema, Results: []*draftverify.Result{}}
	code := exitOK
	for _, pi := range ins {
		in := pi.in
		in.Sandbox, in.Image, in.Limits = sb, f.image, sandbox.Limits{Timeout: f.timeout}
		var r *draftverify.Result
		if pi.refused != "" {
			r = draftverify.Refused(ctx, in, pi.refused)
		} else {
			r = draftverify.Verify(ctx, in)
		}
		if !r.Verified {
			code = exitFindings
		}
		out.Results = append(out.Results, r)
	}
	c := consFlags{out: f.out, format: f.format}
	return code, c.emitReport(out, func(w io.Writer) { writeVerify(w, out.Results) }, stdout, stderr)
}

func writeVerify(w io.Writer, rs []*draftverify.Result) {
	for _, r := range rs {
		v := map[bool]string{true: "verified", false: "NOT verified"}[r.Verified]
		_, _ = fmt.Fprintf(w, "%s %s (claim %s, sandbox %s)\n", v, r.Path, r.ClaimID, r.Sandbox)
		for _, g := range r.Gates {
			_, _ = fmt.Fprintf(w, "  %-8s %-14s %s\n", g.Name, g.Status, g.Detail)
			for _, it := range g.Items {
				if it.Status != draftverify.Pass {
					_, _ = fmt.Fprintf(w, "    line %d %s %s: %s %s\n", it.Line, it.Kind, it.Subject, it.Status, it.Detail)
				}
			}
		}
	}
	_, _ = fmt.Fprintf(w, "docsp draft verify: %d draft(s); nothing applied, no model called\n", len(rs))
}

// placed is one draft turned into a verification input (refused: why it cannot be placed).
type placed struct {
	in      *draftverify.Input
	newText string
	refused string
}

// inputs builds the verification inputs from the flags.
func (f *verifyFlags) inputs(fs *flag.FlagSet) ([]placed, error) {
	if fs.NArg() > 1 || (f.log == "") == (f.path == "") {
		return nil, errors.New("give either -log (drafts log) or -path with -new (a whole-file draft); at most one repository argument")
	}
	root := rootArg(fs)
	name := f.repoName
	if name == "" {
		name = repoBase(root)
	}
	var l *ledger.Ledger
	if f.ledger != "" {
		var err error
		if l, err = ledger.Load(f.ledger); err != nil {
			return nil, err //nolint:wrapcheck // already descriptive
		}
	}
	if f.path != "" {
		return f.wholeFile(root, name, l)
	}
	return f.fromLog(root, name, l)
}

// fromLog places the drafts log's proposed drafts of the repository (or the one of -claim).
func (f *verifyFlags) fromLog(root, name string, l *ledger.Ledger) ([]placed, error) {
	dl, err := draft.LoadLog(f.log)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	var out []placed
	for i := range dl.Entries {
		e := &dl.Entries[i]
		if e.Repo != name || e.Status != draft.Proposed || f.claim != "" && e.ClaimID != f.claim {
			continue
		}
		p, err := placeEntry(root, name, l, e)
		if err != nil {
			return nil, err
		}
		p.in.Ref = f.ref
		out = append(out, p)
	}
	if f.claim != "" && len(out) == 0 {
		return nil, fmt.Errorf("no proposed draft of claim %s for repository %s in %s", f.claim, name, f.log)
	}
	return out, nil
}

func (f *verifyFlags) wholeFile(root, name string, l *ledger.Ledger) ([]placed, error) {
	if f.newFile == "" {
		return nil, errors.New("-path needs -new")
	}
	cur, err := readDoc(root, f.path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(f.newFile)
	if err != nil {
		return nil, fmt.Errorf("-new: %w", err)
	}
	in := &draftverify.Input{Root: root, RepoName: name, Path: f.path, Old: cur, New: string(b), BaseBlob: f.baseBlob, Ledger: l, Ref: f.ref}
	return []placed{{in: in, newText: string(b)}}, nil
}

// placeEntry applies a logged line draft to the document as it is now.
func placeEntry(root, name string, l *ledger.Ledger, e *draft.Entry) (placed, error) {
	cur, err := readDoc(root, e.Path)
	if err != nil {
		return placed{}, err
	}
	in := &draftverify.Input{Root: root, RepoName: name, Path: e.Path, Old: cur, BaseBlob: e.DocBlob, ClaimID: e.ClaimID, Ledger: l}
	if in.BaseBlob == "" && l != nil {
		if c := l.Find(e.ClaimID); c != nil {
			in.BaseBlob = c.DocBlob
		}
	}
	nt, err := draftverify.ApplyLine(cur, e.Line, e.Original, e.Draft, e.Action == draft.ActionDelete)
	if err != nil {
		return placed{in: in, refused: err.Error()}, nil
	}
	in.New = nt
	return placed{in: in, newText: nt}, nil
}

func readDoc(root, p string) (string, error) {
	c, err := judge.CleanPath(p)
	if err != nil {
		return "", fmt.Errorf("document: %w", err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", fmt.Errorf("repo: %w", err)
	}
	defer func() { _ = r.Close() }()
	b, err := r.ReadFile(filepath.FromSlash(c))
	if err != nil {
		return "", fmt.Errorf("document: %w", err)
	}
	return string(b), nil
}

// cmdDraftApply writes one draft into the checkout - only when the document is still the
// blob the draft was made against and the result parses.
func cmdDraftApply(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("draft apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f verifyFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if f.log != "" && f.claim == "" {
		return exitUsage, errors.New("apply one draft: -log needs -claim")
	}
	ins, err := f.inputs(fs)
	if err != nil {
		return exitUsage, err
	}
	p := ins[0]
	if p.refused != "" {
		return exitFindings, fmt.Errorf("apply %s: %s", p.in.Path, p.refused)
	}
	if err := draftverify.Apply(ctx, p.in.Root, p.in.Path, p.in.BaseBlob, p.newText); err != nil {
		return exitFindings, err //nolint:wrapcheck // already descriptive
	}
	_, err = fmt.Fprintf(stdout, "docsp draft apply: %s updated (not committed); run docsp draft verify before applying\n", p.in.Path)
	if err != nil {
		return exitUsage, fmt.Errorf("write: %w", err)
	}
	return exitOK, nil
}
