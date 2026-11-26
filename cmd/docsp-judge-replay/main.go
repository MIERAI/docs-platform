// Command docsp-judge-replay is an offline judge/v1 provider for the exec transport: it
// answers agentic requests (repo_drift, claim_facts, doc_facts) from a file of canned model
// answers and never calls a model. Use it to test the agentic pipeline end to end, or to
// re-score a frozen run through the current harness (the harness re-validates every answer).
//
//	docsp-judge-replay -answers FILE [-probe]
//
// FILE is JSON Lines, one canned answer per line:
//
//	{"key": "drift:<repo>", "answer": {"drift": [...]}}
//	{"key": "line:<repo>:<path>:<line>", "answer": {"facts": [...], "reason": "..."}}
//
// A doc_facts request is answered from the line keys of its claims. With -probe, every
// request first makes one judge-tools call (read_file of the document, or git_changes for a
// drift scan), so the tool round trip is exercised too. A request without a canned answer
// gets a bad_reply failure (answer_parse "failed").
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/MIERAI/docs-platform/judgev1"
)

type canned struct {
	Key    string          `json:"key"`
	Answer json.RawMessage `json:"answer"`
}

func main() {
	answers := flag.String("answers", "", "JSON Lines file of canned answers (required)")
	probe := flag.Bool("probe", false, "make one tool call per request before answering")
	flag.Parse()
	if err := run(*answers, *probe, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "docsp-judge-replay:", err)
		os.Exit(1)
	}
}

func run(file string, probe bool, in io.Reader, out io.Writer) error {
	if file == "" {
		return errors.New("-answers is required")
	}
	m, err := load(file)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	w := bufio.NewWriter(out)
	for sc.Scan() {
		var req judgev1.Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		if probe && req.Mode == judgev1.ModeAgentic {
			if err := toolProbe(&req, w, sc); err != nil {
				return err
			}
		}
		if err := writeLine(w, answer(&req, m)); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	return nil
}

func load(file string) (map[string]json.RawMessage, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("answers: %w", err)
	}
	defer func() { _ = f.Close() }()
	m := map[string]json.RawMessage{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var c canned
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil || c.Key == "" {
			return nil, fmt.Errorf("answers line %d: want {\"key\": ..., \"answer\": {...}}", n)
		}
		m[c.Key] = c.Answer
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("answers: %w", err)
	}
	return m, nil
}

func lineKey(c judgev1.Claim, repo string) string {
	if c.Repo != "" {
		repo = c.Repo
	}
	return fmt.Sprintf("line:%s:%s:%d", repo, c.Path, c.Line)
}

func answer(req *judgev1.Request, m map[string]json.RawMessage) *judgev1.Response {
	model := judgev1.Model{Provider: "replay", ID: "canned"}
	u := judgev1.Usage{Billing: judgev1.BillingNone}
	var a json.RawMessage
	switch req.Task.Type {
	case judgev1.TaskRepoDrift:
		a = m["drift:"+req.Claim.Repo]
	case judgev1.TaskClaimFacts:
		a = m[lineKey(req.Claim, "")]
	case judgev1.TaskDocFacts:
		a = docAnswer(req, m)
	default:
		return judgev1.Failure(req, model, judgev1.ErrCodeConfig, "replay answers agentic structured tasks only")
	}
	obj, parse := judgev1.ParseObject(a, "")
	return judgev1.NewStructuredResponse(req, obj, parse, model, u)
}

func docAnswer(req *judgev1.Request, m map[string]json.RawMessage) json.RawMessage {
	var lines []json.RawMessage
	for _, c := range req.Claims {
		a, ok := m[lineKey(c, req.Claim.Repo)]
		if !ok {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(a, &obj) != nil {
			continue
		}
		obj["line"], _ = json.Marshal(c.Line)
		b, _ := json.Marshal(obj)
		lines = append(lines, b)
	}
	if len(lines) == 0 {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"lines": lines})
	return b
}

func toolProbe(req *judgev1.Request, w *bufio.Writer, sc *bufio.Scanner) error {
	call := judgev1.ToolCall{
		Type: "tool_call", ID: req.ID, Call: "probe-1", Tool: judgev1.ToolReadFile,
		Args: mustJSON(map[string]any{"path": req.Claim.Path, "from_line": max(1, req.Claim.Line-3), "to_line": req.Claim.Line + 3}),
	}
	if req.Task.Type == judgev1.TaskRepoDrift {
		call.Tool, call.Args = judgev1.ToolGitChanges, mustJSON(map[string]any{"kind": "DR", "max_count": 5})
	}
	if err := writeLine(w, call); err != nil {
		return err
	}
	if !sc.Scan() {
		return errors.New("no tool result")
	}
	var res judgev1.ToolResult
	if err := json.Unmarshal(sc.Bytes(), &res); err != nil || res.Type != "tool_result" {
		return errors.New("expected a tool_result line")
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func writeLine(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
