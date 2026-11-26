package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/contract"
	"github.com/MIERAI/docs-platform/internal/instance"
)

// globFlag is a repeatable glob flag.
type globFlag []string

func (g *globFlag) String() string { return strings.Join(*g, ",") }

func (g *globFlag) Set(v string) error {
	*g = append(*g, v)
	return nil
}

// sideFlags locate one side of a contract.
type sideFlags struct {
	dir, ref, repo, adapter string
	paths                   globFlag
}

type contractFlags struct {
	instanceFile, group, docSet string
	checkouts                   checkoutFlag
	design, code                sideFlags
	authority, generator        string
	noHistory                   bool
	workers                     int
	out, format                 string
}

func (f *contractFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.instanceFile, "instance-config", "", "instance configuration with a group whose doc set has a contract: block")
	fs.StringVar(&f.group, "group", "", "group name (default: the only group)")
	fs.StringVar(&f.docSet, "doc-set", "", "the doc set (default: the only doc set with a contract)")
	fs.Var(f.checkouts, "checkout", "repo=DIR: a clone of a group repository (repeatable; it need not be at the revision: docsp reads with git show)")
	for _, s := range []struct {
		side string
		v    *sideFlags
	}{{"design", &f.design}, {"code", &f.code}} {
		fs.StringVar(&s.v.dir, s.side, "", s.side+" side: repository directory")
		fs.StringVar(&s.v.ref, s.side+"-ref", "", s.side+" side: revision read with git show (\"\" = the working tree, uncommitted changes included)")
		fs.StringVar(&s.v.repo, s.side+"-repo", "", s.side+" side: logical repository name (default: directory name)")
		fs.StringVar(&s.v.adapter, s.side+"-adapter", "", s.side+" side adapter: "+strings.Join(contract.AdapterNames(), ", "))
		fs.Var(&s.v.paths, s.side+"-path", s.side+" side: path glob (repeatable; default: everything)")
	}
	fs.StringVar(&f.authority, "authority", "", "which side is right: design, code or ask (default: the doc set's, else ask)")
	fs.StringVar(&f.generator, "generator", "", "how to generate the design from the code (shown when the code is the authority)")
	fs.BoolVar(&f.noHistory, "no-history", false, "skip the direction classification from git history")
	fs.IntVar(&f.workers, "workers", runtime.NumCPU(), "concurrent git calls for the history classification")
	fs.StringVar(&f.out, "out", "", "write the contract/v1 JSON here (mode 0600)")
	fs.StringVar(&f.format, "format", "text", "stdout format: text, json or none")
}

// cmdContract diffs a design-first specification against the code that implements it and
// tells from git history which side moved (no model).
func cmdContract(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("contract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := contractFlags{checkouts: checkoutFlag{}}
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if f.instanceFile != "" {
		if err := f.fromInstance(); err != nil {
			return exitUsage, err
		}
	}
	if f.design.dir == "" || f.code.dir == "" || f.design.adapter == "" || f.code.adapter == "" {
		return exitUsage, errors.New("usage: docsp contract -instance-config FILE [-doc-set NAME] [-checkout repo=DIR], or -design DIR -design-adapter A -code DIR -code-adapter B [-design-ref/-code-ref REV] [-design-path/-code-path GLOB]")
	}
	design, err := contract.OpenSource(ctx, f.design.repo, f.design.dir, f.design.ref, f.design.paths)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	code, err := contract.OpenSource(ctx, f.code.repo, f.code.dir, f.code.ref, f.code.paths)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	doc, err := contract.Run(ctx, contract.Options{
		DocSet: f.docSet, Authority: f.authority, Generator: f.generator,
		DesignAdapter: f.design.adapter, CodeAdapter: f.code.adapter, Design: design, Code: code,
		History: !f.noHistory, Workers: f.workers, Now: time.Now(),
	})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	c := consFlags{out: f.out, format: f.format}
	return exitOK, c.emitReport(doc, func(w io.Writer) { writeContract(w, doc) }, stdout, stderr)
}

// fromInstance fills the flags left empty from the doc set's contract: block.
func (f *contractFlags) fromInstance() error {
	in, err := instance.Load(f.instanceFile)
	if err != nil {
		return err //nolint:wrapcheck // already descriptive
	}
	g, ds, err := findContractDocSet(in, f.group, f.docSet)
	if err != nil {
		return err
	}
	c := ds.Contract
	f.docSet = ds.Name
	dir := func(name string) string {
		if d := f.checkouts[name]; d != "" {
			return d
		}
		for _, r := range g.Repos {
			if r.Name == name {
				return r.Path
			}
		}
		return ""
	}
	setIfEmpty(&f.design.dir, dir(ds.Repo))
	setIfEmpty(&f.design.repo, ds.Repo)
	setIfEmpty(&f.design.ref, firstOf(c.DesignRef, "HEAD"))
	setIfEmpty(&f.design.adapter, c.DesignAdapter)
	if len(f.design.paths) == 0 {
		f.design.paths = ds.Docs
	}
	target := instance.Target{Repo: ds.Repo, Ref: "HEAD"}
	if len(ds.Targets) > 0 {
		target = ds.Targets[0]
	}
	setIfEmpty(&f.code.dir, dir(target.Repo))
	setIfEmpty(&f.code.repo, target.Repo)
	setIfEmpty(&f.code.ref, target.Ref)
	setIfEmpty(&f.code.adapter, c.CodeAdapter)
	if len(f.code.paths) == 0 {
		f.code.paths = c.CodePaths
	}
	setIfEmpty(&f.authority, c.Authority)
	setIfEmpty(&f.generator, c.Generator)
	if f.design.dir == "" || f.code.dir == "" {
		return fmt.Errorf("doc set %s: no checkout of %s or %s (pass -checkout repo=DIR)", ds.Name, ds.Repo, target.Repo)
	}
	return nil
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func findContractDocSet(in *instance.Instance, group, docSet string) (instance.Group, instance.DocSet, error) {
	var found []struct {
		g instance.Group
		d instance.DocSet
	}
	for _, g := range in.Groups {
		if group != "" && g.Name != group {
			continue
		}
		for _, d := range g.DocSets {
			if d.Contract != nil && (docSet == "" || d.Name == docSet) {
				found = append(found, struct {
					g instance.Group
					d instance.DocSet
				}{g, d})
			}
		}
	}
	if len(found) != 1 {
		return instance.Group{}, instance.DocSet{}, fmt.Errorf("want exactly one doc set with a contract: block (found %d; use -group / -doc-set)", len(found))
	}
	return found[0].g, found[0].d, nil
}

func writeContract(w io.Writer, d *contract.Document) {
	s := d.Summary
	_, _ = fmt.Fprintf(w, "docsp contract: %s (authority %s)\n", firstOf(d.DocSet, "-"), d.Authority)
	_, _ = fmt.Fprintf(w, "  design %s@%s (%s): %d operations; code %s@%s (%s): %d operations; overlap %d\n",
		d.Design.Source.Repo, d.Design.Source.Ref, d.Design.Adapter.Name, s.DesignOperations,
		d.Code.Source.Repo, d.Code.Source.Ref, d.Code.Adapter.Name, s.CodeOperations, s.Overlap)
	_, _ = fmt.Fprintf(w, "  %d items: %s\n", s.Items, counts(s.BySeverity))
	if len(s.ByDirection) > 0 {
		_, _ = fmt.Fprintf(w, "  direction: %s\n", counts(s.ByDirection))
	}
	if n := len(d.Design.Gaps) + len(d.Code.Gaps); n > 0 {
		_, _ = fmt.Fprintf(w, "  %d files could not be parsed (their operations are unknown)\n", n)
	}
	for _, it := range d.Items {
		if it.Severity == contract.SevInfo {
			continue
		}
		dir := ""
		if it.Direction != nil {
			dir = " [" + it.Direction.Class + "]"
		}
		_, _ = fmt.Fprintf(w, "  %-6s %s%s\n", it.Severity, it.Message, dir)
	}
}

func counts(m map[string]int) string {
	var parts []string
	for _, k := range sortedMapKeys(m) {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func sortedMapKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
