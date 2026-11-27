// Package ci tests ci/run.sh, the shell half of the composite action (ci/action.yml), with
// a stub docsp: which commands it runs, with which flags, and what reaches the job summary,
// the annotations and the step's exit status. No model, no network, no GitHub.
package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocsp records its argv and writes the files the real one would.
const fakeDocsp = `#!/usr/bin/env bash
echo "$*" >>"$RUNNER_TEMP/calls"
cmd=$1; shift
out=""; ann=""; sum=""
while [ $# -gt 0 ]; do
	case $1 in
	-out) out=$2; shift ;;
	-annotations-out) ann=$2; shift ;;
	-summary-out) sum=$2; shift ;;
	esac
	shift
done
case $cmd in
schedule)
	[ -n "${FAKE_SCHEDULE_FAIL:-}" ] && exit 1
	echo '{"picked":[{"id":"a","priority":0},{"id":"b","priority":2}]}' >"$out" ;;
recheck)
	[ -n "${FAKE_RECHECK_FAIL:-}" ] && exit 1
	echo '{"judged":1}' >"$out"; echo "docsp recheck: 1 judged" ;;
pr-comment)
	echo "{\"problems\":[${FAKE_PROBLEMS:-}]}" >"$out"
	echo "::warning file=README.md,line=3,title=docs-platform%3A x::y" >"$ann"
	echo "### docs-platform: full list" >"$sum"
	echo "docsp pr-comment: done" ;;
esac
`

type result struct {
	code            int
	stdout, summary string
	calls           string
}

func runPR(t *testing.T, env ...string) result {
	t.Helper()
	for _, p := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skipf("%s not installed", p)
		}
	}
	tmp := t.TempDir()
	write := func(p, s string, mode os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(tmp, "docsp"), fakeDocsp, 0o700)
	write(filepath.Join(tmp, "docsp-ledger", "ledger.json"), "{}", 0o600)
	write(filepath.Join(tmp, "bin", "claude"), "#!/bin/sh\n", 0o700)
	write(filepath.Join(tmp, "summary.md"), "", 0o600)
	cmd := exec.Command("bash", "run.sh", "pr")
	cmd.Env = append([]string{
		"PATH=" + filepath.Join(tmp, "bin") + ":" + os.Getenv("PATH"), "HOME=" + tmp,
		"RUNNER_TEMP=" + tmp, "GITHUB_STEP_SUMMARY=" + filepath.Join(tmp, "summary.md"), "GITHUB_OUTPUT=" + filepath.Join(tmp, "out"),
		"GITHUB_REPOSITORY=o/r", "GITHUB_RUN_ID=42", "GITHUB_SERVER_URL=https://github.com",
		"DOCSP_TASK=pr", "DOCSP_FORK=false", "DOCSP_AI=true", "DOCSP_RUN_CAP=1", "DOCSP_MODEL=m", "DOCSP_PR=5",
		"DOCSP_SKIP_LABEL=docsp:skip", `DOCSP_LABELS=["bug"]`,
	}, env...)
	var so strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &so
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	sum, _ := os.ReadFile(filepath.Join(tmp, "summary.md"))
	calls, _ := os.ReadFile(filepath.Join(tmp, "calls"))
	return result{code, so.String(), string(sum), string(calls)}
}

const incomplete = "incomplete: this does not mean everything is fine"

func TestSkipLabel(t *testing.T) {
	r := runPR(t, `DOCSP_LABELS=["bug","docsp:skip"]`)
	if r.code != 0 || r.calls != "" || !strings.Contains(r.summary, "has the label docsp:skip: not checked") {
		t.Errorf("%+v", r)
	}
}

func TestNeutralByDefaultWithAnnotationsAndSummary(t *testing.T) {
	r := runPR(t, "FAKE_PROBLEMS={}", "DOCSP_LANGUAGE=en", "DOCSP_MAX_PROBLEMS=3")
	if r.code != 0 {
		t.Fatalf("neutral: the step passes with problems: %+v", r)
	}
	for _, s := range []string{"-language en", "-max-problems 3", "-summary-url https://github.com/o/r/actions/runs/42", "-schedule ", "-recheck ", "-post-pr 5"} {
		if !strings.Contains(r.calls, s) {
			t.Errorf("pr-comment lacks %q: %s", s, r.calls)
		}
	}
	if !strings.Contains(r.stdout, "\n::warning file=README.md,line=3,") || !strings.Contains(r.summary, "### docs-platform: full list") {
		t.Errorf("annotations / summary: %+v", r)
	}
	if strings.Contains(r.summary, incomplete) {
		t.Errorf("a complete run is not called incomplete: %s", r.summary)
	}
}

func TestConclusionFailure(t *testing.T) {
	if r := runPR(t, "FAKE_PROBLEMS={}", "DOCSP_CONCLUSION=failure"); r.code == 0 {
		t.Errorf("failure with problems must fail the step: %+v", r)
	}
	if r := runPR(t, "DOCSP_CONCLUSION=failure"); r.code != 0 {
		t.Errorf("failure without problems passes: %+v", r)
	}
}

func TestIncompleteRunSaysSo(t *testing.T) {
	r := runPR(t, "FAKE_RECHECK_FAIL=1")
	if r.code != 0 || !strings.Contains(r.calls, "-incomplete aborted") || !strings.Contains(r.summary, incomplete) {
		t.Errorf("a failed recheck: %+v", r)
	}
	r = runPR(t, "FAKE_SCHEDULE_FAIL=1")
	if !strings.Contains(r.calls, "-incomplete aborted") || strings.Contains(r.calls, "recheck") {
		t.Errorf("a failed schedule: %+v", r)
	}
	r = runPR(t, "DOCSP_AI=false")
	if r.calls != "" || !strings.Contains(r.summary, incomplete) {
		t.Errorf("no AI: %+v", r)
	}
}
