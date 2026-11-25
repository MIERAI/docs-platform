package judge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/judgev1"
)

// TestReadFileLineNumbersAreCitable is the regression test of the v0.4 TEST run: read_file
// returned an unnumbered JSON array, models counted lines wrongly and their quotes were
// rejected. The rendered result now shows each line's number; a quote cited at that number
// must verify, and repository text must arrive unescaped.
func TestReadFileLineNumbersAreCitable(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 120; i++ {
		b.WriteString("filler line " + strconv.Itoa(i) + "\n")
	}
	b.WriteString("    - id: host-b   # clusters/<name> && a -> b\n")
	if err := os.WriteFile(filepath.Join(dir, "conf.yml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tl, _ := NewTools(dir, nil, judgev1.ToolLimits{})
	defer func() { _ = tl.Close() }()
	r := call(t, tl, judgev1.ToolReadFile, `{"path":"conf.yml","from_line":100}`)
	if !r.OK || strings.Contains(string(r.Content), `\u003c`) || !strings.Contains(r.Text, "clusters/<name> && a -> b") {
		t.Fatalf("escaped or failed: %s %s", r.Error, r.Text)
	}
	m := regexp.MustCompile(`(?m)^(\d+): (.*host-b.*)$`).FindStringSubmatch(r.Text)
	if m == nil || m[1] != "121" || !strings.HasPrefix(r.Text, "conf.yml (lines 100-121 of 121)") {
		t.Fatalf("rendered read_file:\n%s", r.Text)
	}
	n, _ := strconv.Atoi(m[1])
	if v := tl.VerifyNear("conf.yml", n, m[2]); !v.OK {
		t.Fatalf("a quote cited at the rendered line number was rejected: %s", v.Reason)
	}
	part := call(t, tl, judgev1.ToolReadFile, `{"path":"conf.yml","from_line":1,"to_line":3}`)
	if !strings.HasSuffix(part.Text, "3: filler line 3\n[more: read from_line 4]") {
		t.Fatalf("partial read:\n%s", part.Text)
	}
}

func TestRenderResults(t *testing.T) {
	dir := worktree(t)
	gitIn(t, dir, "mv", "README.md", "GUIDE.md")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "rename")
	tl, _ := NewTools(dir, publicOnly, judgev1.ToolLimits{MaxSteps: 100, MaxResultBytes: 2000})
	defer func() { _ = tl.Close() }()
	tests := []struct{ tool, args, want string }{
		{judgev1.ToolSearch, `{"pattern":"const port"}`, "main.go:4: const port = 9090"},
		{judgev1.ToolSearch, `{"pattern":"nowhere-to-be-found"}`, "no matches ("},
		{judgev1.ToolGrep, `{"pattern":"port = [0-9]+"}`, "main.go:4: const port = 9090"},
		{judgev1.ToolListDir, `{"path":"."}`, "GUIDE.md\nbig.txt"},
		{judgev1.ToolGlob, `{"pattern":"*.go"}`, "main.go"},
		{judgev1.ToolGitLog, `{"path":"."}`, " rename"},
		{judgev1.ToolGitChanges, `{"kind":"R"}`, "| R100 README.md -> GUIDE.md"},
		{judgev1.ToolGitShow, `{"rev":"HEAD~1","path":"README.md"}`, "\n3: The port is 8080."},
		{judgev1.ToolOutline, `{"path":"main.go"}`, "4: const port = 9090"},
		{judgev1.ToolReadFile, `{"path":"big.txt"}`, truncNote},
	}
	for _, tt := range tests {
		r := call(t, tl, tt.tool, tt.args)
		if !r.OK || !strings.Contains(r.Text, tt.want) || strings.HasPrefix(r.Text, "{") {
			t.Errorf("%s %s: ok=%v %s\n%s", tt.tool, tt.args, r.OK, r.Error, cutStr(r.Text, 400))
		}
	}
}

func TestOutlineAndConfigValue(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"docs/guide.md":    "# Guide\n\ntext\n\n## Setup\n| a | b |\n|---|---|\n| 1 | 2 |\n",
		"group_vars/x.yml": "watch:\n  port: 9195\n  targets:\n    - id: laptop\n    - id: host-b\nport: 80\n",
		"Makefile":         "GO ?= go\nbuild:\n\t$(GO) build\n",
		"main.go":          "package main\n\nvar interval = flag.Duration(\"interval\", time.Minute, \"poll\")\nconst Port = 8080\n",
		"secret/k.yml":     "port: 1\n",
	}
	for p, c := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o750)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tl, _ := NewTools(dir, func(p string) bool { return !strings.HasPrefix(p, "secret") }, judgev1.ToolLimits{MaxSteps: 100})
	defer func() { _ = tl.Close() }()
	tests := []struct{ tool, args, want, not string }{
		{judgev1.ToolOutline, `{"path":"docs/guide.md"}`, "1: # Guide\n5: ## Setup\n6: | a | b |", ""},
		{judgev1.ToolOutline, `{"path":"group_vars/x.yml"}`, "2:   port: 9195", ""},
		{judgev1.ToolOutline, `{"path":"Makefile"}`, "2: build:", ""},
		{judgev1.ToolOutline, `{"path":"docs"}`, "guide.md", ""},
		{judgev1.ToolConfigValue, `{"key":"watch.port"}`, "group_vars/x.yml:2: watch.port = 9195", "x.yml:6"},
		{judgev1.ToolConfigValue, `{"key":"port"}`, "group_vars/x.yml:6: port = 80", "secret/k.yml"},
		{judgev1.ToolConfigValue, `{"key":"targets"}`, "watch.targets = (mapping/list below)", ""},
		{judgev1.ToolConfigValue, `{"key":"interval"}`, `main.go:3: var interval = flag.Duration("interval"`, ""},
		{judgev1.ToolConfigValue, `{"key":"nothing_here"}`, "no definition/value found", ""},
	}
	for _, tt := range tests {
		r := call(t, tl, tt.tool, tt.args)
		if !r.OK || !strings.Contains(r.Text, tt.want) || tt.not != "" && strings.Contains(r.Text, tt.not) {
			t.Errorf("%s %s: ok=%v %s\n%s", tt.tool, tt.args, r.OK, r.Error, r.Text)
		}
	}
	if r := call(t, tl, judgev1.ToolOutline, `{"path":"secret/k.yml"}`); r.OK {
		t.Error("outline of a private file")
	}
}

func TestMCPShowsRenderedText(t *testing.T) {
	tl, _ := NewTools(worktree(t), publicOnly, judgev1.ToolLimits{MaxSteps: 10})
	defer func() { _ = tl.Close() }()
	var out strings.Builder
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"main.go"}}}`
	if err := ServeMCP(context.Background(), strings.NewReader(in), &out, tl); err != nil {
		t.Fatal(err)
	}
	var r struct {
		Result struct {
			Content []mcpContent `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out.String()), &r); err != nil || len(r.Result.Content) != 1 ||
		!strings.Contains(r.Result.Content[0].Text, "\n4: const port = 9090") {
		t.Fatalf("%v %s", err, out.String())
	}
}
