package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/judgev1"
)

func request(id, task string, claims ...judgev1.Claim) string {
	r := judgev1.Request{
		SchemaVersion: judgev1.SchemaVersion, ID: id, Task: judgev1.Task{Type: task}, Claim: claims[0],
		Mode: judgev1.ModeAgentic, Prompt: judgev1.Prompt{Hash: "0123456789abcdef", User: "u"},
	}
	if task == judgev1.TaskDocFacts {
		r.Claims = claims
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func TestReplay(t *testing.T) {
	answers := filepath.Join(t.TempDir(), "a.jsonl")
	if err := os.WriteFile(answers, []byte(`{"key":"drift:demo","answer":{"drift":[]}}
{"key":"line:demo:README.md:3","answer":{"facts":[],"reason":"three"}}
{"key":"line:demo:README.md:5","answer":{"facts":[],"reason":"five"}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := func(line int) judgev1.Claim {
		return judgev1.Claim{Repo: "demo", Path: "README.md", Line: line, Text: "x"}
	}
	// the provider's stdin: requests, each preceded (probe) by the core's tool result
	result := `{"type":"tool_result","id":"x","call":"probe-1","ok":true}`
	in := strings.Join([]string{
		request("d", judgev1.TaskRepoDrift, judgev1.Claim{Repo: "demo", Path: ".", Text: "r"}), result,
		request("l3", judgev1.TaskClaimFacts, c(3)), result,
		request("g", judgev1.TaskDocFacts, c(3), c(4), c(5)), result,
		request("l9", judgev1.TaskClaimFacts, c(9)), result,
	}, "\n") + "\n"
	pr, pw := io.Pipe()
	go func() { _, _ = io.WriteString(pw, in); _ = pw.Close() }()
	var out strings.Builder
	if err := run(answers, true, pr, &out); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	var lines []map[string]any
	for sc.Scan() {
		var m map[string]any
		_ = json.Unmarshal(sc.Bytes(), &m)
		lines = append(lines, m)
	}
	if len(lines) != 8 {
		t.Fatalf("%d lines:\n%s", len(lines), out.String())
	}
	if lines[0]["tool"] != judgev1.ToolGitChanges || lines[2]["tool"] != judgev1.ToolReadFile {
		t.Fatalf("probes %v %v", lines[0], lines[2])
	}
	if lines[1]["answer_parse"] != judgev1.ParseSchema || lines[3]["answer"].(map[string]any)["reason"] != "three" {
		t.Fatalf("answers %v %v", lines[1], lines[3])
	}
	doc := lines[5]["answer"].(map[string]any)["lines"].([]any)
	if len(doc) != 2 || doc[1].(map[string]any)["line"].(float64) != 5 {
		t.Fatalf("doc answer %v", lines[5])
	}
	if lines[7]["answer_parse"] != judgev1.ParseFailed || lines[7]["error"].(map[string]any)["code"] != judgev1.ErrCodeBadReply {
		t.Fatalf("missing answer %v", lines[7])
	}
	if err := run("", false, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("-answers not required")
	}
}
