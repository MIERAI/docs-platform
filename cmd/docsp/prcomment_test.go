package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MIERAI/docs-platform/internal/prtext"
)

func TestIncompleteReasons(t *testing.T) {
	dir := t.TempDir()
	put := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sched := put("s.json", `{"schema_version":"1.0","repo":"r","ref":"h","planned_at":"","counts":{},"estimated_usd":0,`+
		`"picked":[{"id":"a","priority":0},{"id":"b","priority":0},{"id":"c","priority":0}],"shortfall":[{"id":"d","priority":0}]}`)
	short := put("r1.json", `{"judged":1,"retired":0,"failed":1,"categories":{}}`)
	full := put("r2.json", `{"judged":3,"retired":0,"failed":0,"categories":{}}`)
	noShort := put("s2.json", `{"picked":[{"id":"a","priority":0}],"shortfall":[{"id":"x","priority":1}]}`)
	for _, tt := range []struct {
		name, sched, recheck, stated string
		want                         []string
	}{
		{"complete", noShort, put("r3.json", `{"judged":1}`), "", nil},
		{"budget shortfall, failures, missing", sched, short, "", []string{prtext.ReasonBudget, prtext.ReasonJudgeFailures, prtext.ReasonPartial}},
		{"stated by the caller, once", sched, full, "aborted, budget", []string{prtext.ReasonAborted, prtext.ReasonBudget}},
		{"nothing given", "", "", "", nil},
	} {
		got, err := incompleteReasons(tt.sched, tt.recheck, tt.stated)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: %v %v, want %v", tt.name, got, err, tt.want)
		}
	}
	if _, err := incompleteReasons("", put("bad.json", "{"), ""); err == nil {
		t.Error("a broken recheck file is an error")
	}
}
