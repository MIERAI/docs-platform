package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/contract"
)

// labSpec is bench/contractlab/lab.json: drifts injected into scratch clones (never
// committed) with the items the contract diff must report.
type labSpec struct {
	Description string    `json:"description"`
	Repos       []labRepo `json:"repos"`
}

type labRepo struct {
	Name        string        `json:"name"`
	URL         string        `json:"url"`
	Commit      string        `json:"commit"`
	DesignPaths []string      `json:"design_paths"`
	CodePaths   []string      `json:"code_paths"`
	Mutations   []labMutation `json:"mutations"`
}

type labMutation struct {
	ID     string      `json:"id"`
	What   string      `json:"what"`
	File   string      `json:"file"`
	Old    string      `json:"old"`
	New    string      `json:"new"`
	All    bool        `json:"all,omitempty"`
	Alarm  bool        `json:"alarm"`
	Expect []labExpect `json:"expect,omitempty"`
}

type labExpect struct {
	Operation string `json:"operation"`
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Direction string `json:"direction"`
}

// labResult is one mutation's outcome.
type labResult struct {
	Repo      string   `json:"repo"`
	ID        string   `json:"id"`
	Alarm     bool     `json:"alarm"`
	Detected  bool     `json:"detected"`
	Direction bool     `json:"direction_ok"`
	Missed    []string `json:"missed,omitempty"`
	WrongDir  []string `json:"wrong_direction,omitempty"`
	Secondary []string `json:"secondary,omitempty"`
	False     []string `json:"false_alarms,omitempty"`
}

// labReport is the lab's result document.
type labReport struct {
	Baseline   map[string]int `json:"baseline_items"`
	Results    []labResult    `json:"results"`
	Mutations  int            `json:"drift_mutations"`
	Detected   int            `json:"detected"`
	DirectionO int            `json:"direction_correct"`
	NoAlarm    int            `json:"no_alarm_mutations"`
	Quiet      int            `json:"no_alarm_quiet"`
	FalseItems int            `json:"false_alarm_items"`
	Secondary  int            `json:"secondary_items"`
}

// runContractLab: go run ./bench contract-lab -lab FILE -work DIR [-json FILE].
func runContractLab(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("contract-lab", flag.ContinueOnError)
	labFile := fs.String("lab", "bench/contractlab/lab.json", "lab specification")
	work := fs.String("work", "", "scratch directory for the clones (outside any repository)")
	out := fs.String("json", "", "write the result JSON here")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("flags: %w", err)
	}
	if *work == "" {
		return errors.New("-work is required")
	}
	b, err := os.ReadFile(*labFile)
	if err != nil {
		return fmt.Errorf("lab: %w", err)
	}
	var spec labSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return fmt.Errorf("%s: %w", *labFile, err)
	}
	rep := labReport{Baseline: map[string]int{}}
	for _, r := range spec.Repos {
		if err := labRunRepo(ctx, r, *work, &rep); err != nil {
			return fmt.Errorf("%s: %w", r.Name, err)
		}
	}
	printLab(rep)
	if *out != "" {
		js, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*out, append(js, '\n'), 0o600); err != nil {
			return fmt.Errorf("result: %w", err)
		}
	}
	return nil
}

func labRunRepo(ctx context.Context, r labRepo, work string, rep *labReport) error {
	dir := filepath.Join(work, r.Name)
	if err := labClone(ctx, r, dir); err != nil {
		return err
	}
	base, err := labDiff(ctx, r, dir)
	if err != nil {
		return err
	}
	rep.Baseline[r.Name] = len(base)
	for _, m := range r.Mutations {
		res, err := labMutate(ctx, r, dir, m, base)
		if err != nil {
			return fmt.Errorf("%s: %w", m.ID, err)
		}
		rep.Results = append(rep.Results, res)
		rep.add(res)
	}
	return nil
}

func (rep *labReport) add(res labResult) {
	rep.FalseItems += len(res.False)
	rep.Secondary += len(res.Secondary)
	if !res.Alarm {
		rep.NoAlarm++
		if len(res.False) == 0 {
			rep.Quiet++
		}
		return
	}
	rep.Mutations++
	if res.Detected {
		rep.Detected++
	}
	if res.Detected && res.Direction {
		rep.DirectionO++
	}
}

// labClone makes (or reuses) a clone at the pinned commit.
func labClone(ctx context.Context, r labRepo, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if out, err := exec.CommandContext(ctx, "git", "clone", "-q", r.URL, dir).CombinedOutput(); err != nil { //nolint:gosec // URL from the lab file
			return fmt.Errorf("clone: %s", out)
		}
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "checkout", "-q", "--detach", r.Commit).CombinedOutput(); err != nil { //nolint:gosec // pinned commit from the lab file
		return fmt.Errorf("checkout %s: %s", r.Commit, out)
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output(); err != nil || len(out) > 0 { //nolint:gosec // fixed argv
		return fmt.Errorf("%s is not clean (a previous run was interrupted?): %s", dir, out)
	}
	return nil
}

// labDiff runs the contract diff on the working tree (uncommitted changes included).
func labDiff(ctx context.Context, r labRepo, dir string) (map[string]contract.Item, error) {
	design, err := contract.OpenSource(ctx, r.Name, dir, "", r.DesignPaths)
	if err != nil {
		return nil, fmt.Errorf("design: %w", err)
	}
	code, err := contract.OpenSource(ctx, r.Name, dir, "", r.CodePaths)
	if err != nil {
		return nil, fmt.Errorf("code: %w", err)
	}
	doc, err := contract.Run(ctx, contract.Options{
		DesignAdapter: "factory-design", CodeAdapter: "factory-code", Design: design, Code: code,
		History: true, Workers: 8, Now: time.Now(),
	})
	if err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	items := map[string]contract.Item{}
	for _, it := range doc.Items {
		items[labKey(it.Operation, it.Kind, it.Subject)] = it
	}
	return items, nil
}

func labKey(op, kind, subject string) string { return op + " " + kind + " " + subject }

// labMutate applies one mutation, diffs, and restores the file byte for byte.
func labMutate(ctx context.Context, r labRepo, dir string, m labMutation, base map[string]contract.Item) (labResult, error) {
	path := filepath.Join(dir, filepath.FromSlash(m.File))
	if !filepath.IsLocal(filepath.FromSlash(m.File)) {
		return labResult{}, fmt.Errorf("%s: not a path inside the clone", m.File)
	}
	orig, err := os.ReadFile(path)
	if err != nil {
		return labResult{}, fmt.Errorf("read: %w", err)
	}
	n := strings.Count(string(orig), m.Old)
	if n == 0 || (n > 1 && !m.All) {
		return labResult{}, fmt.Errorf("snippet occurs %d times in %s", n, m.File)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(orig), m.Old, m.New)), 0o600); err != nil { //nolint:gosec // a local path inside the scratch clone (checked above)
		return labResult{}, fmt.Errorf("mutate: %w", err)
	}
	items, derr := labDiff(ctx, r, dir)
	if err := os.WriteFile(path, orig, 0o600); err != nil { //nolint:gosec // a local path inside the scratch clone (checked above)
		return labResult{}, fmt.Errorf("restore: %w", err)
	}
	if derr != nil {
		return labResult{}, derr
	}
	return labScore(r.Name, m, base, items), nil
}

func labScore(repo string, m labMutation, base, items map[string]contract.Item) labResult {
	res := labResult{Repo: repo, ID: m.ID, Alarm: m.Alarm, Detected: m.Alarm, Direction: true}
	want := map[string]bool{}
	ops := map[string]bool{}
	for _, e := range m.Expect {
		k := labKey(e.Operation, e.Kind, e.Subject)
		want[k], ops[e.Operation] = true, true
		it, ok := items[k]
		if !ok {
			res.Detected, res.Direction = false, false
			res.Missed = append(res.Missed, k)
			continue
		}
		if got := dirOf(it); got != e.Direction {
			res.Direction = false
			res.WrongDir = append(res.WrongDir, k+": "+got)
		}
	}
	for _, k := range sortedItemKeys(items) {
		if _, ok := base[k]; ok || want[k] {
			continue
		}
		if m.Alarm && ops[items[k].Operation] {
			res.Secondary = append(res.Secondary, k+" ["+dirOf(items[k])+"]")
			continue
		}
		res.False = append(res.False, k)
	}
	return res
}

func dirOf(it contract.Item) string {
	if it.Direction == nil {
		return ""
	}
	return it.Direction.Class
}

func sortedItemKeys(m map[string]contract.Item) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func printLab(rep labReport) {
	for _, r := range rep.Results {
		status := "ok"
		switch {
		case r.Alarm && !r.Detected:
			status = "MISSED"
		case r.Alarm && !r.Direction:
			status = "WRONG-DIRECTION"
		case len(r.False) > 0:
			status = "FALSE-ALARM"
		}
		fmt.Printf("%-16s %-12s %-28s secondary=%d false=%d\n", status, r.Repo, r.ID, len(r.Secondary), len(r.False))
		for _, x := range append(append(append([]string(nil), r.Missed...), r.WrongDir...), r.False...) {
			fmt.Printf("    %s\n", x)
		}
	}
	fmt.Printf("baseline items %v\n", rep.Baseline)
	fmt.Printf("drifts detected %d/%d, direction correct %d/%d; no-alarm changes quiet %d/%d; false-alarm items %d; secondary items on the drifted operation %d\n",
		rep.Detected, rep.Mutations, rep.DirectionO, rep.Mutations, rep.Quiet, rep.NoAlarm, rep.FalseItems, rep.Secondary)
}
