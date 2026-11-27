package onboard

import (
	"fmt"
	"math"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/MIERAI/docs-platform/internal/repo"
)

func execLookPath(name string) (string, error) {
	p, err := exec.LookPath(name)
	return p, err //nolint:wrapcheck // the caller reports only that it is missing
}

// ActionRef is what a workflow step's `uses:` contains when it runs the CI action.
const ActionRef = "docs-platform/ci"

// ExampleWorkflow is the minimal workflow shipped in the repository.
const ExampleWorkflow = "examples/docs-platform.yml"

type workflow struct {
	On          any            `yaml:"on"`
	Permissions any            `yaml:"permissions"`
	Jobs        map[string]job `yaml:"jobs"`
}

type job struct {
	Permissions any    `yaml:"permissions"`
	Steps       []step `yaml:"steps"`
}

type step struct {
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

// checkWorkflows finds the workflows that run the CI action and checks each one.
func checkWorkflows(rep *Report, root string) {
	r, err := repo.Open(root, nil)
	if err != nil {
		return
	}
	found := 0
	for _, f := range r.AllFiles() {
		if !strings.HasPrefix(f, ".github/workflows/") || (path.Ext(f) != ".yml" && path.Ext(f) != ".yaml") {
			continue
		}
		b, err := r.Read(f)
		if err != nil || !strings.Contains(string(b), ActionRef) {
			continue
		}
		var w workflow
		if err := yaml.Unmarshal(b, &w); err != nil {
			rep.add("workflow", Fail, "%s: %v", f, err)
			continue
		}
		found += checkWorkflow(rep, f, &w)
	}
	if found == 0 {
		rep.add("workflow", Warn, "no workflow runs MIERAI/%s; %s is a 15-line start", ActionRef, ExampleWorkflow)
	}
}

// checkWorkflow checks every job that runs the action; it returns how many it found.
func checkWorkflow(rep *Report, file string, w *workflow) int {
	n := 0
	names := make([]string, 0, len(w.Jobs))
	for k := range w.Jobs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		j := w.Jobs[name]
		i := actionStep(j.Steps)
		if i < 0 {
			continue
		}
		n++
		where := fmt.Sprintf("%s (job %s)", file, name)
		perms := j.Permissions
		if perms == nil {
			perms = w.Permissions
		}
		checkPermissions(rep, where, perms)
		checkCheckout(rep, where, j.Steps[:i])
		checkInputs(rep, where, j.Steps[i].With)
		checkTriggers(rep, where, w.On)
	}
	return n
}

func actionStep(steps []step) int {
	for i, s := range steps {
		if strings.Contains(s.Uses, ActionRef) || s.Uses == "./ci" {
			return i
		}
	}
	return -1
}

// needed are the token permissions the action uses: the checkout, the PR comment and the
// spend artifacts that make up the monthly budget.
var needed = []struct {
	scope, want, why string
}{
	{"contents", "read", "the checkout"},
	{"pull-requests", "write", "the PR comment"},
	{"actions", "read", "the monthly budget (summed from this month's spend artifacts) and the outcome stream (downloaded from the docsp-outcomes artifact)"},
}

func checkPermissions(rep *Report, where string, perms any) {
	switch p := perms.(type) {
	case nil:
		rep.add("permissions", Warn, "%s: no permissions block, so the token's rights depend on the repository settings; declare contents: read, pull-requests: write, actions: read", where)
		return
	case string:
		if p == "write-all" {
			rep.add("permissions", OK, "%s: write-all (more than needed: contents: read, pull-requests: write, actions: read)", where)
			return
		}
		rep.add("permissions", Fail, "%s: permissions %q cannot comment on a PR; declare contents: read, pull-requests: write, actions: read", where, p)
		return
	case map[string]any:
		bad := 0
		for _, n := range needed {
			got, _ := p[n.scope].(string)
			if !grants(got, n.want) {
				bad++
				rep.add("permissions", Fail, "%s: %s: %s is needed for %s (have %q)", where, n.scope, n.want, n.why, got)
			}
		}
		if bad == 0 {
			rep.add("permissions", OK, "%s: contents, pull-requests and actions permissions suffice", where)
		}
	default:
		rep.add("permissions", Fail, "%s: permissions is neither a map nor read-all/write-all", where)
	}
}

func grants(got, want string) bool {
	return got == want || got == "write"
}

func checkCheckout(rep *Report, where string, before []step) {
	for _, s := range before {
		if !strings.Contains(s.Uses, "actions/checkout") {
			continue
		}
		if fmt.Sprint(s.With["fetch-depth"]) == "0" {
			rep.add("checkout", OK, "%s: full history (fetch-depth: 0)", where)
		} else {
			rep.add("checkout", Warn, "%s: actions/checkout without fetch-depth: 0; the PR mode needs the merge base with the base branch", where)
		}
		return
	}
	rep.add("checkout", Fail, "%s: no actions/checkout step before the action: there is nothing to check", where)
}

// checkInputs checks the task, the token input and that the budget inputs are sane.
func checkInputs(rep *Report, where string, with map[string]any) {
	task := fmt.Sprint(with["task"])
	switch {
	case with["task"] == nil:
		rep.add("inputs", Fail, "%s: the task input is required (pr, main or factory-pr)", where)
	case !expr(task) && task != "pr" && task != "main" && task != "factory-pr":
		rep.add("inputs", Fail, "%s: task %q: want pr, main or factory-pr", where, task)
	}
	if with["claude-oauth-token"] == nil {
		rep.add("token", Warn, "%s: no claude-oauth-token input: deterministic only, no AI judgement", where)
	}
	usd := map[string]float64{}
	for _, k := range []string{"monthly-usd", "run-usd", "bootstrap-usd"} {
		v, ok := with[k]
		if !ok || expr(fmt.Sprint(v)) {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			rep.add("budget", Fail, "%s: %s %q is not a non-negative number of USD", where, k, fmt.Sprint(v))
			continue
		}
		usd[k] = f
	}
	checkBudget(rep, where, usd)
}

func checkBudget(rep *Report, where string, usd map[string]float64) {
	m, hasM := usd["monthly-usd"]
	if !hasM {
		m = 20 // the action's default
	}
	switch {
	case m == 0:
		rep.add("budget", Info, "%s: monthly-usd is 0: no AI ever runs", where)
	case usd["run-usd"] > m:
		rep.add("budget", Warn, "%s: run-usd (%g) is above monthly-usd (%g): one run would spend the month", where, usd["run-usd"], m)
	case usd["bootstrap-usd"] > m:
		rep.add("budget", Warn, "%s: bootstrap-usd (%g) is above monthly-usd (%g): the first ledger build is capped by the month", where, usd["bootstrap-usd"], m)
	default:
		rep.add("budget", OK, "%s: budget inputs are sane (monthly $%g)", where, m)
	}
}

func expr(s string) bool { return strings.Contains(s, "${{") }

// checkTriggers: pull_request gives the PR mode (ledger-less until a ledger exists); push to
// the default branch builds the ledger, kept in the Actions cache (evicted after 7 days
// unused, so a weekly schedule keeps it).
func checkTriggers(rep *Report, where string, on any) {
	ev := events(on)
	switch {
	case !ev["pull_request"] && !ev["push"]:
		rep.add("triggers", Warn, "%s: neither pull_request nor push triggers the workflow", where)
	case !ev["push"]:
		rep.add("triggers", Info, "%s: PR only: every PR runs the ledger-less mode; add push (task main) to build the ledger, the upgrade path", where)
	case !ev["schedule"]:
		rep.add("cache", Info, "%s: the ledger lives in the Actions cache, evicted after 7 days unused; a weekly schedule keeps it", where)
	default:
		rep.add("triggers", OK, "%s: pull_request, push and schedule", where)
	}
}

func events(on any) map[string]bool {
	out := map[string]bool{}
	switch v := on.(type) {
	case string:
		out[v] = true
	case []any:
		for _, e := range v {
			out[fmt.Sprint(e)] = true
		}
	case map[string]any:
		for k := range v {
			out[k] = true
		}
	}
	return out
}
