package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/MIERAI/docs-platform/judgev1"
)

// Agentic loop limits for local models.
const (
	// ctxFraction: once a turn's prompt fills this share of num_ctx, the model must answer.
	ctxFraction = 0.65
	// maxChatReply bounds one /api/chat reply.
	maxChatReply = 4 << 20
	finalNudge   = "Tool budget exhausted or exploration finished. Reply now with the final JSON object only, following the schema."
	// agenticNumPredict: the answer of an agentic request is a list of facts, not one verdict.
	agenticNumPredict = 4096
	repeatNotice      = "You already made exactly this call; its result is above. Try something different or give the final answer."
)

type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
}

type chatToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatRequest struct {
	Model    string          `json:"model"`
	Messages []chatMessage   `json:"messages"`
	Tools    []chatTool      `json:"tools,omitempty"`
	Format   json.RawMessage `json:"format,omitempty"`
	Stream   bool            `json:"stream"`
	Think    bool            `json:"think"`
	Options  genOptions      `json:"options"`
}

type chatResponse struct {
	Model           string      `json:"model"`
	Message         chatMessage `json:"message"`
	Error           string      `json:"error"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
}

// chatTools converts the judge-tools definitions into Ollama's tool list.
func chatTools() []chatTool {
	out := make([]chatTool, 0, len(toolDefs))
	for _, d := range toolDefs {
		var t chatTool
		t.Type = "function"
		t.Function.Name, t.Function.Description, t.Function.Parameters = d.Name, d.Description, d.InputSchema
		out = append(out, t)
	}
	return out
}

// agenticLoop explores the worktree with native tool calls, then asks for the final answer
// with the reply schema as Ollama's structured-output format (no tools in that call). The
// tolerant parser is the fallback when the reply still is not a bare object.
type agenticLoop struct {
	o     *Ollama
	req   *judgev1.Request
	tools *Tools
	msgs  []chatMessage
	seen  map[string]bool
	usage judgev1.Usage
	model string
}

func (o *Ollama) judgeAgentic(ctx context.Context, req *judgev1.Request) (*judgev1.Response, error) {
	t := ToolsFrom(ctx)
	if t == nil {
		return nil, errors.New("ollama: agentic mode needs the worktree tools")
	}
	l := &agenticLoop{o: o, req: req, tools: t, seen: map[string]bool{}, model: o.cfg.Model,
		msgs:  []chatMessage{{Role: "system", Content: req.Prompt.System}, {Role: "user", Content: req.Prompt.User}},
		usage: judgev1.Usage{Billing: judgev1.BillingNone}}
	steps := t.Limits().MaxSteps
	if req.Limits.MaxTurns > 0 {
		steps = min(steps, req.Limits.MaxTurns)
	}
	if err := l.explore(ctx, steps); err != nil {
		return nil, err
	}
	l.msgs = append(l.msgs, chatMessage{Role: "user", Content: finalNudge})
	r, err := l.chat(ctx, false, req.Prompt.ReplySchema)
	if err != nil {
		return nil, err
	}
	m := judgev1.Model{Provider: o.Name(), ID: l.model}
	if !judgev1.Structured(req.Task.Type) {
		a, err := judgev1.ParseAnswer(req.Task.Type, r.Message.Content)
		if err != nil {
			return judgev1.Failure(req, m, judgev1.ErrCodeBadReply, err.Error()), nil
		}
		return judgev1.NewResponse(req, a, m, l.usage), nil
	}
	obj, parse := judgev1.ParseObject(nil, r.Message.Content)
	if parse == judgev1.ParseFallback && json.Valid(bytes.TrimSpace([]byte(r.Message.Content))) {
		parse = judgev1.ParseSchema // the format-constrained reply was the bare object
	}
	return judgev1.NewStructuredResponse(req, obj, parse, m, l.usage), nil
}

// explore runs tool turns until the model stops calling tools, the step budget is spent or
// the context is nearly full.
func (l *agenticLoop) explore(ctx context.Context, steps int) error {
	calls := 0
	limit := int(float64(l.o.cfg.NumCtx) * ctxFraction)
	for calls < steps {
		r, err := l.chat(ctx, true, nil)
		if err != nil {
			return err
		}
		l.msgs = append(l.msgs, chatMessage{Role: "assistant", Content: r.Message.Content, ToolCalls: r.Message.ToolCalls})
		if len(r.Message.ToolCalls) == 0 {
			return nil
		}
		for _, tc := range r.Message.ToolCalls {
			calls++
			l.msgs = append(l.msgs, chatMessage{Role: "tool", ToolName: tc.Function.Name, Content: l.call(ctx, tc)})
		}
		if r.PromptEvalCount > limit {
			return nil
		}
	}
	return nil
}

func (l *agenticLoop) call(ctx context.Context, tc chatToolCall) string {
	args := tc.Function.Arguments
	if len(args) > 0 && args[0] == '"' { // some models send the arguments as a JSON string
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = json.RawMessage(s)
		}
	}
	key := tc.Function.Name + "\x00" + string(args)
	if l.seen[key] {
		return repeatNotice
	}
	l.seen[key] = true
	res := l.tools.Call(ctx, judgev1.ToolCall{Type: "tool_call", ID: l.req.ID, Tool: tc.Function.Name, Args: args})
	if !res.OK {
		return "error: " + res.Error
	}
	return string(res.Content)
}

func (l *agenticLoop) chat(ctx context.Context, withTools bool, format json.RawMessage) (chatResponse, error) {
	body := chatRequest{
		Model: l.o.cfg.Model, Messages: l.msgs, Format: format,
		Options: genOptions{Seed: l.o.cfg.Seed, NumCtx: l.o.cfg.NumCtx, NumPredict: max(l.o.cfg.NumPredict, agenticNumPredict)},
	}
	if withTools {
		body.Tools = chatTools()
	}
	b, err := json.Marshal(body)
	if err != nil {
		return chatResponse{}, fmt.Errorf("ollama: encode chat: %w", err)
	}
	var last error
	for attempt := 0; attempt <= l.o.cfg.Retries; attempt++ {
		r, retry, err := l.once(ctx, b)
		if err == nil {
			l.usage.InputTokens += r.PromptEvalCount
			l.usage.OutputTokens += r.EvalCount
			if r.Model != "" {
				l.model = r.Model
			}
			return r, nil
		}
		if !retry {
			return chatResponse{}, err
		}
		last = err
	}
	return chatResponse{}, last
}

func (l *agenticLoop) once(ctx context.Context, body []byte) (chatResponse, bool, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, l.o.base.JoinPath("api", "chat").String(), bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, false, fmt.Errorf("ollama request: %w", err)
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := l.o.client.Do(hr)
	if err != nil {
		return chatResponse{}, ctx.Err() == nil, fmt.Errorf("ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxChatReply))
	if err != nil {
		return chatResponse{}, true, fmt.Errorf("ollama read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return chatResponse{}, resp.StatusCode >= 500, fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}
	var r chatResponse
	if err := json.Unmarshal(b, &r); err != nil {
		return chatResponse{}, false, fmt.Errorf("ollama decode: %w", err)
	}
	if r.Error != "" {
		return chatResponse{}, false, fmt.Errorf("ollama: %s", r.Error)
	}
	return r, false, nil
}
