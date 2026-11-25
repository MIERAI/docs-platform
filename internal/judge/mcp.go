package judge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/MIERAI/docs-platform/judgev1"
)

// MCPServerName is the name the judge-tools server has in a CLI's MCP configuration; its
// tools appear to the model as mcp__docsp__<tool>.
const MCPServerName = "docsp"

// MCPSpec is everything an out-of-process judge-tools server needs for one request: the
// worktree, who may see what, the limits, the documentation under test, and where to append
// its transcript. It is written by the core to a private (0600) file.
type MCPSpec struct {
	Root       string             `json:"root"`
	Policy     Policy             `json:"policy"`
	Limits     judgev1.ToolLimits `json:"limits"`
	Doc        Doc                `json:"doc"`
	Transcript string             `json:"transcript"`
}

// WriteMCPSpec writes the spec of t to file (mode 0600); the transcript goes to transcript.
func WriteMCPSpec(t *Tools, file, transcript string) error {
	if t.policy == nil {
		return errors.New("judge tools: an out-of-process tool server needs a classification policy (NewPolicyTools)")
	}
	b, err := json.Marshal(MCPSpec{Root: t.dir, Policy: *t.policy, Limits: t.limits, Doc: t.docRef(), Transcript: transcript})
	if err != nil {
		return fmt.Errorf("encode tool spec: %w", err)
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return fmt.Errorf("write tool spec: %w", err)
	}
	return nil
}

// OpenMCPSpec opens the tools a spec file describes; every step is appended (JSON Lines,
// mode 0600) to the spec's transcript file.
func OpenMCPSpec(file string) (*Tools, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read tool spec: %w", err)
	}
	var s MCPSpec
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode tool spec: %w", err)
	}
	t, err := NewPolicyTools(s.Root, s.Policy, s.Limits)
	if err != nil {
		return nil, err
	}
	t.SetDoc(s.Doc)
	if s.Transcript != "" {
		var mu sync.Mutex
		t.record = func(st judgev1.Step) {
			mu.Lock()
			defer mu.Unlock()
			f, err := os.OpenFile(s.Transcript, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return
			}
			defer func() { _ = f.Close() }()
			line, _ := json.Marshal(st)
			_, _ = f.Write(append(line, '\n'))
		}
	}
	return t, nil
}

// ReadTranscript reads a transcript file written by an out-of-process tool server.
func ReadTranscript(file string) []judgev1.Step {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out []judgev1.Step
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var st judgev1.Step
		if json.Unmarshal(sc.Bytes(), &st) == nil {
			out = append(out, st)
		}
	}
	return out
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// maxMCPLine bounds one JSON-RPC message from the client.
const maxMCPLine = 4 << 20

// ServeMCP answers a minimal MCP session (JSON-RPC 2.0, one message per line, stdio
// transport) with the judge-tools of t: initialize, tools/list and tools/call. Everything
// else with an id is "method not found"; notifications are ignored. It returns when in ends.
func ServeMCP(ctx context.Context, in io.Reader, out io.Writer, t *Tools) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), maxMCPLine)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var m rpcMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 || string(m.ID) == "null" {
			continue // garbage or a notification
		}
		r := rpcReply{JSONRPC: "2.0", ID: m.ID}
		switch m.Method {
		case "initialize":
			r.Result = initResult(m.Params)
		case "ping":
			r.Result = map[string]any{}
		case "tools/list":
			var ts []mcpTool
			for _, d := range toolDefs {
				ts = append(ts, mcpTool{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema})
			}
			r.Result = map[string]any{"tools": ts}
		case "tools/call":
			r.Result = callResult(ctx, t, m.Params)
		default:
			r.Error = &rpcError{Code: -32601, Message: "method not found"}
		}
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("mcp: write: %w", err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("mcp: read: %w", err)
	}
	return nil
}

func initResult(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	if p.ProtocolVersion == "" {
		p.ProtocolVersion = "2024-11-05"
	}
	return map[string]any{
		"protocolVersion": p.ProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": MCPServerName, "version": ToolsContract},
	}
}

func callResult(ctx context.Context, t *Tools, params json.RawMessage) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return map[string]any{"content": []mcpContent{{Type: "text", Text: "error: bad tools/call parameters"}}, "isError": true}
	}
	res := t.Call(ctx, judgev1.ToolCall{Type: "tool_call", Tool: p.Name, Args: p.Arguments})
	if !res.OK {
		return map[string]any{"content": []mcpContent{{Type: "text", Text: "error: " + res.Error}}, "isError": true}
	}
	text := string(res.Content)
	if res.Truncated {
		text += "\n(truncated: narrow the request)"
	}
	return map[string]any{"content": []mcpContent{{Type: "text", Text: text}}}
}
