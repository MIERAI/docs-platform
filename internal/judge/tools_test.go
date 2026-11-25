package judge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/judgev1"
)

// worktree builds a small git repository with a public and a private part.
func worktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"main.go":         "package main\n\n// port is the listen port.\nconst port = 9090\n",
		"README.md":       "# Demo\n\nThe port is 8080.\n",
		"private/keys.md": "secret plan with port\n",
		"big.txt":         strings.Repeat("port line\n", 3000),
	}
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc", filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func publicOnly(p string) bool { return !strings.HasPrefix(p, "private") }

func call(t *testing.T, tl *Tools, tool, args string) judgev1.ToolResult {
	t.Helper()
	return tl.Call(context.Background(), judgev1.ToolCall{Type: "tool_call", ID: "r", Call: "c", Tool: tool, Args: json.RawMessage(args)})
}

func TestToolsConfinement(t *testing.T) {
	tl, err := NewTools(worktree(t), publicOnly, judgev1.ToolLimits{MaxSteps: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tl.Close() }()
	tests := []struct {
		name, tool, args, wantErr string
	}{
		{"parent", judgev1.ToolReadFile, `{"path":"../x"}`, "outside"},
		{"absolute", judgev1.ToolReadFile, `{"path":"/etc/passwd"}`, "repository-relative"},
		{"dot-git", judgev1.ToolReadFile, `{"path":".git/config"}`, "outside"},
		{"symlink escape", judgev1.ToolReadFile, `{"path":"escape/hosts"}`, "escape"},
		{"option injection", judgev1.ToolGitLog, `{"path":"--output=/tmp/x"}`, "repository-relative"},
		{"private", judgev1.ToolReadFile, `{"path":"private/keys.md"}`, "private"},
		{"unknown arg", judgev1.ToolReadFile, `{"path":"main.go","mode":"w"}`, "unknown field"},
		{"unknown tool", "write_file", `{}`, "unknown tool"},
		{"bad rev", judgev1.ToolGitShow, `{"rev":"--all"}`, "invalid revision"},
		{"bad regexp", judgev1.ToolGrep, `{"pattern":"("}`, "grep"},
	}
	for _, tt := range tests {
		r := call(t, tl, tt.tool, tt.args)
		if r.OK || !strings.Contains(r.Error, tt.wantErr) {
			t.Errorf("%s: ok=%v err=%q", tt.name, r.OK, r.Error)
		}
	}
}

func TestToolsResults(t *testing.T) {
	tl, err := NewTools(worktree(t), publicOnly, judgev1.ToolLimits{MaxSteps: 6, MaxResultBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tl.Close() }()
	tests := []struct {
		name, tool, args, want string
		truncated              bool
	}{
		{"list root hides private and .git", judgev1.ToolListDir, `{}`, `"main.go"`, false},
		{"read window", judgev1.ToolReadFile, `{"path":"main.go","from_line":4,"to_line":4}`, `"lines":["const port = 9090"]`, false},
		{"grep skips private", judgev1.ToolGrep, `{"pattern":"port =","max_results":5}`, `"path":"main.go","line":4`, false},
		{"big file truncated", judgev1.ToolReadFile, `{"path":"big.txt"}`, `"truncated`, true},
		{"git log", judgev1.ToolGitLog, `{"path":"main.go"}`, `"subject":"init"`, false},
		{"git show file", judgev1.ToolGitShow, `{"rev":"HEAD","path":"main.go"}`, `const port = 9090`, false},
	}
	for _, tt := range tests {
		r := call(t, tl, tt.tool, tt.args)
		if !r.OK || r.Truncated != tt.truncated || (!tt.truncated && !strings.Contains(string(r.Content), tt.want)) {
			t.Errorf("%s: ok=%v trunc=%v err=%q content=%.200s", tt.name, r.OK, r.Truncated, r.Error, r.Content)
		}
		if strings.Contains(string(r.Content), "secret plan") || strings.Contains(string(r.Content), `"private"`) {
			t.Errorf("%s: private content leaked", tt.name)
		}
	}
	if r := call(t, tl, judgev1.ToolListDir, `{}`); r.OK || !strings.Contains(r.Error, "budget") {
		t.Errorf("step budget not enforced: %+v", r)
	}
	if st := tl.Transcript(); len(st) != 7 || st[0].Tool != judgev1.ToolListDir || st[1].ResultBytes == 0 {
		t.Errorf("transcript %+v", st)
	}
}

func TestToolsVerify(t *testing.T) {
	tl, err := NewTools(worktree(t), publicOnly, judgev1.ToolLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tl.Close() }()
	tests := []struct {
		name string
		ref  judgev1.EvidenceRef
		ok   bool
	}{
		{"exact", judgev1.EvidenceRef{Path: "main.go", Line: 4, Quote: "const port = 9090"}, true},
		{"within 2", judgev1.EvidenceRef{Path: "main.go", Line: 2, Quote: "const port = 9090"}, true},
		{"too far", judgev1.EvidenceRef{Path: "main.go", Line: 1, Quote: "const port = 9090"}, false},
		{"invented", judgev1.EvidenceRef{Path: "main.go", Line: 4, Quote: "const port = 1234"}, false},
		{"at rev", judgev1.EvidenceRef{Path: "main.go", Line: 4, Quote: "const port = 9090", Rev: "HEAD"}, true},
		{"private", judgev1.EvidenceRef{Path: "private/keys.md", Line: 1, Quote: "secret plan with port"}, false},
	}
	for _, tt := range tests {
		_, err := tl.Verify(context.Background(), []judgev1.EvidenceRef{tt.ref})
		if (err == nil) != tt.ok {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}

// fakeAgent is the DOCSP_FAKE=agent provider: it greps through the core's tools, then answers
// with the match as evidence (or, for "lie", with an invented quote).
func fakeAgent() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<22)
	for in.Scan() {
		var req judgev1.Request
		_ = json.Unmarshal(in.Bytes(), &req)
		c, _ := json.Marshal(judgev1.ToolCall{Type: "tool_call", ID: req.ID, Call: "1", Tool: judgev1.ToolGrep, Args: json.RawMessage(`{"pattern":"const port"}`)})
		fmt.Println(string(c))
		in.Scan()
		var res judgev1.ToolResult
		_ = json.Unmarshal(in.Bytes(), &res)
		var g grepResult
		_ = json.Unmarshal(res.Content, &g)
		ref := judgev1.EvidenceRef{Path: "main.go", Line: 4, Quote: "const port = 1234"}
		if len(g.Matches) > 0 && !strings.Contains(req.Claim.Text, "lie") {
			ref = judgev1.EvidenceRef{Path: g.Matches[0].Path, Line: g.Matches[0].Line, Quote: g.Matches[0].Text}
		}
		resp := judgev1.NewResponse(&req, judgev1.Answer{Verdict: judgev1.Yes}, judgev1.Model{Provider: "agent", ID: "m"}, judgev1.Usage{})
		resp.EvidenceRefs = []judgev1.EvidenceRef{ref}
		b, _ := json.Marshal(resp)
		fmt.Println(string(b))
	}
}

func TestAgenticRoundTrip(t *testing.T) {
	t.Setenv("DOCSP_FAKE", "agent")
	p, err := NewExec(ExecConfig{
		Name: "exec", Model: "m", Command: []string{self(t)}, PassEnv: []string{"DOCSP_FAKE"},
		Cloud: true, Modes: []string{judgev1.ModeAgentic}, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := NewGuard("judge", p, testInstance(t, ""))
	defer func() { _ = g.Close() }()
	tl, err := NewTools(worktree(t), publicOnly, judgev1.ToolLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tl.Close() }()
	ctx := WithTools(context.Background(), tl)
	tests := []struct{ claim, verdict string }{{"The port is 8080", judgev1.Yes}, {"lie: port 8080", judgev1.Unknown}}
	for i, tt := range tests {
		req := NewRequest(fmt.Sprint("a", i), judgev1.TaskClaimContradiction, judgev1.Claim{Repo: "pub", Path: "README.md", Line: 3, Text: tt.claim}, nil, "")
		req.Mode = judgev1.ModeAgentic
		o, err := g.Judge(ctx, req)
		if err != nil || o.Verdict != tt.verdict || len(o.Response.Transcript) == 0 {
			t.Fatalf("%s: %+v %v", tt.claim, o, err)
		}
	}
	snippet, _ := NewExec(ExecConfig{Name: "exec", Model: "m", Command: []string{self(t)}, Timeout: time.Second})
	sg, _ := NewGuard("judge", snippet, testInstance(t, ""))
	req := NewRequest("s", judgev1.TaskClaimContradiction, judgev1.Claim{Repo: "pub", Path: "README.md", Text: "x"}, nil, "")
	req.Mode = judgev1.ModeAgentic
	if _, err := sg.Judge(ctx, req); err == nil || !strings.Contains(err.Error(), "does not support agentic") {
		t.Fatalf("snippet-only provider accepted agentic mode: %v", err)
	}
}

func FuzzCleanPath(f *testing.F) {
	for _, s := range []string{"a/b", "../x", "/etc", "a/../../b", ".git/x", "-rf", "a\x00b", `a\..\b`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, err := CleanPath(s)
		if err != nil {
			return
		}
		if strings.HasPrefix(c, "/") || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "-") || strings.HasPrefix(c, ".git/") {
			t.Fatalf("CleanPath(%q) = %q escapes", s, c)
		}
	})
}

func FuzzToolCall(f *testing.F) {
	f.Add(judgev1.ToolGrep, `{"pattern":"a+"}`)
	f.Add(judgev1.ToolReadFile, `{"path":"a","from_line":-5,"to_line":99999999}`)
	f.Add(judgev1.ToolListDir, `{"path":"../.."}`)
	dir := f.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("alpha\nbeta\n"), 0o600)
	tl, err := NewTools(dir, nil, judgev1.ToolLimits{MaxSteps: 1 << 30, MaxTotalBytes: 1 << 40})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, tool, args string) {
		if tool == judgev1.ToolGitLog || tool == judgev1.ToolGitShow {
			return // needs a repository; covered by TestToolsResults
		}
		r := tl.Call(context.Background(), judgev1.ToolCall{Tool: tool, Args: json.RawMessage(args)})
		if r.OK && len(r.Content) > DefaultMaxResultBytes {
			t.Fatalf("result over the cap: %d bytes", len(r.Content))
		}
	})
}
