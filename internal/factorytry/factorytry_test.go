package factorytry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const result = `{"schema": "factory.try-result/v1", "base": "abc", "status": "ok", "stage": "accept",
 "apis": [{"id": "a.calc", "cases": [
   {"name": "e1", "source": "example", "passed": false, "detail": "sla: want 1 got 0"},
   {"name": "l1", "source": "logic_example", "passed": true},
   {"name": "h1", "source": "hand_test", "passed": false}]}],
 "summary": {"passed": 1, "failed": 2, "by_source": {}}}`

func TestParseAndCount(t *testing.T) {
	r, err := Parse([]byte(result))
	if err != nil {
		t.Fatal(err)
	}
	if f := r.Failed(Design...); len(f) != 1 || f[0].Name != "e1" {
		t.Errorf("design failures %+v", f)
	}
	if len(r.Failed()) != 2 || r.Ran(Design...) != 2 || r.Ran(SourceAuto) != 0 {
		t.Errorf("failed %d, design ran %d", len(r.Failed()), r.Ran(Design...))
	}
	for _, bad := range []string{`{"schema": "x/v1", "status": "ok"}`, `{"schema": "factory.try-result/v1"}`, `{`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

// The command gets the request through a file and reads the result back.
func TestCommand(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "factory.sh")
	// args: try --changes REQ --out OUT --api a.calc
	body := "#!/bin/sh\n[ \"$1\" = try ] || exit 9\ngrep -q '\"path\":\"logic.go\"' \"$3\" || exit 8\n[ \"$7\" = a.calc ] || exit 7\ncat > \"$5\" <<'J'\n" + result + "\nJ\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &Command{Argv: []string{script}, Dir: dir}
	r, err := c.Try(context.Background(), []File{{Path: "logic.go", Content: "x"}}, []string{"a.calc"})
	if err != nil || r.Base != "abc" {
		t.Fatalf("%+v %v", r, err)
	}
	c.Argv = []string{"/bin/false"}
	if _, err := c.Try(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "no result") {
		t.Errorf("a factory that writes no result: %v", err)
	}
}
