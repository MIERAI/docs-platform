package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/MIERAI/docs-platform/judgev1"
)

// ToolCaller runs one judge-tools/v1 call through docs-platform: the call is written to the
// core as a tool_call line and the core's tool_result line is read back. The core executes
// the tool read-only, confined to the inspected worktree, and refuses private paths for this
// (cloud) provider; the adapter never touches the repository itself.
type ToolCaller func(ctx context.Context, c judgev1.ToolCall) (judgev1.ToolResult, error)

// Agentic defaults.
const (
	// agenticMaxOutput: one turn of an agentic request may carry the final answer (a drift
	// list or a fact list) plus adaptive thinking; the core's snippet-sized default is raised.
	agenticMaxOutput = 16000
	// defaultAgenticSteps when neither the tool limits nor the request bound the steps.
	defaultAgenticSteps = 16
	finalNudge          = "Tool budget exhausted. Do not call more tools: reply now with the final JSON object only, following the schema."
)

// errToolChannel: the core stopped answering tool calls.
var errToolChannel = errors.New("tool channel closed")

// Answer answers one request: agentic requests explore the worktree through call, all others
// go to Judge. It never returns an error: failures are judge/v1 responses.
func (a *Adapter) Answer(ctx context.Context, req *judgev1.Request, call ToolCaller) *judgev1.Response {
	if req.Mode != judgev1.ModeAgentic {
		return a.Judge(ctx, req)
	}
	if err := req.Validate(); err != nil {
		return judgev1.Failure(req, a.model(), judgev1.ErrCodeBadReply, err.Error())
	}
	if call == nil || req.Tools == nil || len(req.Tools.Definitions) == 0 {
		return judgev1.Failure(req, a.model(), judgev1.ErrCodeConfig, "agentic mode needs the judge-tools definitions and the tool channel")
	}
	if req.DryRun {
		return a.dryRun(req, a.agenticMaxOut(req))
	}
	if req.Limits.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Limits.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	l := &loop{
		a: a, req: req, call: call, maxOut: a.agenticMaxOut(req), steps: agenticSteps(req),
		tools: toolParams(req.Tools.Definitions), modelID: a.cfg.Model,
		msgs: []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.Prompt.User))},
	}
	l.inTokens = judgev1.EstimateTokens(req.Prompt.System) + judgev1.EstimateTokens(req.Prompt.User) +
		judgev1.EstimateTokens(string(req.Prompt.ReplySchema)) + toolDefTokens(req.Tools.Definitions)
	start := time.Now()
	resp := l.run(ctx)
	resp.LatencyMS = time.Since(start).Milliseconds()
	return resp
}

func (a *Adapter) agenticMaxOut(req *judgev1.Request) int {
	return max(req.Limits.MaxOutputTokens, agenticMaxOutput)
}

// agenticSteps is the tool-step budget: the smaller of the tool limits and the turn limit.
func agenticSteps(req *judgev1.Request) int {
	steps := req.Tools.Limits.MaxSteps
	if t := req.Limits.MaxTurns; t > 0 && (steps <= 0 || t < steps) {
		steps = t
	}
	if steps <= 0 {
		steps = defaultAgenticSteps
	}
	return steps
}

// requestCap is the smaller of the run's remaining budget and the request's own cap, and
// whether the request's own cap is the binding one (reaching it ends this request only).
func requestCap(req *judgev1.Request) (float64, bool, bool) {
	r, c := req.Limits.BudgetRemainingUSD, req.Limits.MaxRequestUSD
	hasR, hasC := r != nil && *r > 0, c != nil && *c > 0
	switch {
	case hasR && hasC:
		return min(*r, *c), *c <= *r, true
	case hasR:
		return *r, false, true
	case hasC:
		return *c, true, true
	}
	return 0, false, false
}

// loop is one agentic request: a manual tool-use loop over the Messages API.
type loop struct {
	a        *Adapter
	req      *judgev1.Request
	call     ToolCaller
	tools    []anthropic.BetaToolUnionParam
	msgs     []anthropic.BetaMessageParam
	usage    judgev1.Usage
	modelID  string
	maxOut   int
	steps    int
	calls    int
	inTokens int // estimate of the conversation so far (the next turn's input)
}

func (l *loop) model() judgev1.Model { return judgev1.Model{Provider: "anthropic", ID: l.modelID} }

func (l *loop) run(ctx context.Context) *judgev1.Response {
	l.usage.Billing = judgev1.BillingAPI
	for {
		if f := l.overBudget(); f != nil {
			return f
		}
		final := l.calls >= l.steps
		msg, err := l.a.client.Beta.Messages.New(ctx, l.params(final))
		if err != nil {
			f := l.a.failure(l.req, ctx, err)
			f.Usage = l.usage
			return f
		}
		l.account(msg)
		switch msg.StopReason {
		case anthropic.BetaStopReasonRefusal:
			return l.fail(judgev1.ErrCodeRefused, "the model declined the request")
		case anthropic.BetaStopReasonMaxTokens:
			return l.structuredFail("answer truncated at max_tokens")
		case anthropic.BetaStopReasonToolUse:
			if final {
				return l.structuredFail("the model kept calling tools after the step budget")
			}
			if err := l.runTools(ctx, msg); err != nil {
				return l.fail(judgev1.ErrCodeTransport, err.Error())
			}
			continue
		default: // end_turn and the rest: read the answer
		}
		return l.answer(msg)
	}
}

// params builds one turn: the core's exact prompts, docsp's read-only tools, structured
// output with the reply schema, a cache breakpoint on the stable prefix (tools + system) and
// automatic caching of the growing conversation. Effort is sent only when configured for
// agentic runs (the agentic design was evaluated at the model's default effort).
func (l *loop) params(final bool) anthropic.BetaMessageNewParams {
	a := l.a
	p := anthropic.BetaMessageNewParams{
		Model:        a.cfg.Model,
		MaxTokens:    int64(l.maxOut),
		System:       []anthropic.BetaTextBlockParam{{Text: l.req.Prompt.System, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}},
		Messages:     l.msgs,
		Tools:        l.tools,
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		OutputConfig: anthropic.BetaOutputConfigParam{Format: anthropic.BetaJSONOutputFormatParam{Schema: l.req.Prompt.ReplySchema}},
	}
	if a.cfg.AgenticEffort != "" {
		p.OutputConfig.Effort = anthropic.BetaOutputConfigEffort(a.cfg.AgenticEffort)
	}
	if final {
		p.ToolChoice = anthropic.BetaToolChoiceUnionParam{OfNone: &anthropic.BetaToolChoiceNoneParam{}}
	}
	if a.cfg.Fallbacks != "none" {
		p.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		p.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	return p
}

// overBudget checks the worst case of the next turn (the conversation so far as input, the
// full output allowance) against the binding cap before it is sent.
func (l *loop) overBudget() *judgev1.Response {
	capUSD, own, ok := requestCap(l.req)
	if !ok || !l.a.priced {
		return nil
	}
	if l.usage.CostUSD+l.a.price.cost(l.inTokens, l.maxOut, 0) <= capUSD {
		return nil
	}
	if own {
		return l.structuredFail("the request's own cost cap was reached before an answer")
	}
	return l.fail(judgev1.ErrCodeBudget, "worst-case cost of the next turn exceeds the remaining budget")
}

func (l *loop) account(msg *anthropic.BetaMessage) {
	if msg.Model != "" {
		l.modelID = msg.Model
	}
	u := msg.Usage
	l.usage.InputTokens += int(u.InputTokens)
	l.usage.OutputTokens += int(u.OutputTokens)
	l.usage.CacheReadInputTokens += int(u.CacheReadInputTokens)
	l.usage.CacheCreationInputTokens += int(u.CacheCreationInputTokens)
	if p, ok := prices[l.modelID]; ok {
		l.usage.CostUSD += p.cost(int(u.InputTokens+u.CacheCreationInputTokens), int(u.OutputTokens), int(u.CacheReadInputTokens))
	}
	l.inTokens += int(u.OutputTokens)
}

// runTools answers every tool_use block of a turn through the core, in one user message.
func (l *loop) runTools(ctx context.Context, msg *anthropic.BetaMessage) error {
	l.msgs = append(l.msgs, msg.ToParam())
	var results []anthropic.BetaContentBlockParamUnion
	for _, b := range msg.Content {
		tu, ok := b.AsAny().(anthropic.BetaToolUseBlock)
		if !ok {
			continue
		}
		l.calls++
		text, isErr := "tool budget exhausted: answer with what you have", true
		if l.calls <= l.steps {
			var err error
			if text, isErr, err = l.tool(ctx, tu); err != nil {
				return err
			}
		}
		l.inTokens += judgev1.EstimateTokens(text)
		results = append(results, anthropic.NewBetaToolResultBlock(tu.ID, text, isErr))
	}
	if l.calls >= l.steps {
		results = append(results, anthropic.NewBetaTextBlock(finalNudge))
	}
	l.msgs = append(l.msgs, anthropic.NewBetaUserMessage(results...))
	return nil
}

// tool runs one call; the model sees the core's model-ready text (numbered lines, no
// escaping), never the JSON content when text is present.
func (l *loop) tool(ctx context.Context, tu anthropic.BetaToolUseBlock) (string, bool, error) {
	args := json.RawMessage(tu.JSON.Input.Raw())
	if !json.Valid(args) {
		args = json.RawMessage(`{}`)
	}
	res, err := l.call(ctx, judgev1.ToolCall{Type: "tool_call", ID: l.req.ID, Call: fmt.Sprintf("c%d", l.calls), Tool: tu.Name, Args: args})
	if err != nil {
		return "", true, err
	}
	if !res.OK {
		return "error: " + res.Error, true, nil
	}
	if res.Text != "" {
		return res.Text, false, nil
	}
	return string(res.Content), false, nil
}

func (l *loop) answer(msg *anthropic.BetaMessage) *judgev1.Response {
	var text strings.Builder
	for _, b := range msg.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	if !judgev1.Structured(l.req.Task.Type) {
		ans, err := judgev1.ParseAnswer(l.req.Task.Type, text.String())
		if err != nil {
			return l.fail(judgev1.ErrCodeBadReply, err.Error())
		}
		return judgev1.NewResponse(l.req, ans, l.model(), l.usage)
	}
	// With structured output the text block is the schema-bound object itself.
	obj, parse := judgev1.ParseObject(json.RawMessage(strings.TrimSpace(text.String())), text.String())
	return judgev1.NewStructuredResponse(l.req, obj, parse, l.model(), l.usage)
}

func (l *loop) fail(code, msg string) *judgev1.Response {
	r := judgev1.Failure(l.req, l.model(), code, msg)
	r.Usage = l.usage
	return r
}

// structuredFail ends this request without an answer (a parse failure for structured tasks),
// never the run.
func (l *loop) structuredFail(msg string) *judgev1.Response {
	if !judgev1.Structured(l.req.Task.Type) {
		return l.fail(judgev1.ErrCodeBadReply, msg)
	}
	r := judgev1.NewStructuredResponse(l.req, nil, judgev1.ParseFailed, l.model(), l.usage)
	r.Error.Message = msg
	return r
}

// toolParams converts the judge-tools definitions of the request into Messages API tools.
func toolParams(defs []judgev1.ToolDef) []anthropic.BetaToolUnionParam {
	out := make([]anthropic.BetaToolUnionParam, 0, len(defs))
	for _, d := range defs {
		var s map[string]any
		_ = json.Unmarshal(d.InputSchema, &s)
		schema := anthropic.BetaToolInputSchemaParam{Properties: s["properties"], ExtraFields: map[string]any{}}
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if name, ok := r.(string); ok {
					schema.Required = append(schema.Required, name)
				}
			}
		}
		for k, v := range s {
			if k != "type" && k != "properties" && k != "required" {
				schema.ExtraFields[k] = v
			}
		}
		t := anthropic.BetaToolParam{Name: d.Name, Description: anthropic.String(d.Description), InputSchema: schema}
		out = append(out, anthropic.BetaToolUnionParam{OfTool: &t})
	}
	return out
}

func toolDefTokens(defs []judgev1.ToolDef) int {
	n := 0
	for _, d := range defs {
		n += judgev1.EstimateTokens(d.Name + d.Description + string(d.InputSchema))
	}
	return n
}

// toolChannel is the ToolCaller of the stdin/stdout transport: a tool_call line out, the
// matching tool_result line in.
func toolChannel(enc *json.Encoder, next func() ([]byte, bool)) ToolCaller {
	return func(_ context.Context, c judgev1.ToolCall) (judgev1.ToolResult, error) {
		if err := enc.Encode(c); err != nil {
			return judgev1.ToolResult{}, fmt.Errorf("write tool call: %w", err)
		}
		line, ok := next()
		if !ok {
			return judgev1.ToolResult{}, errToolChannel
		}
		var r judgev1.ToolResult
		if err := json.Unmarshal(line, &r); err != nil || r.Type != "tool_result" || r.ID != c.ID || r.Call != c.Call {
			return judgev1.ToolResult{}, errors.New("unexpected line on the tool channel")
		}
		return r, nil
	}
}
