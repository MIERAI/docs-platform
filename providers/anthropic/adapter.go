package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/MIERAI/docs-platform/judgev1"
)

// apiHost is the only host requests may go to.
const apiHost = "api.anthropic.com"

// Config of the adapter (all from instance configuration via argv).
type Config struct {
	Model, Effort, KeyFile, KeyEnv, Fallbacks string
	// AgenticEffort is the effort of agentic requests ("" = the model's default). Effort is
	// not inherited by agentic requests: the agentic design was evaluated without one.
	AgenticEffort string
	MaxRetries    int
}

// Adapter answers judge/v1 requests with the Messages API.
type Adapter struct {
	cfg    Config
	client anthropic.Client
	price  price
	priced bool
}

// New validates the configuration, loads the key and builds the client.
func New(c Config) (*Adapter, error) {
	return newAdapter(c, allowOnly(map[string]bool{apiHost: true}, http.DefaultTransport), "")
}

func newAdapter(c Config, rt http.RoundTripper, baseURL string) (*Adapter, error) {
	if c.Model == "" {
		return nil, errors.New("-model is required")
	}
	if !validEffort(c.Effort) {
		return nil, fmt.Errorf("-effort %q: want low, medium, high, xhigh or max", c.Effort)
	}
	if c.AgenticEffort != "" && !validEffort(c.AgenticEffort) {
		return nil, fmt.Errorf("-agentic-effort %q: want low, medium, high, xhigh, max or empty", c.AgenticEffort)
	}
	key, err := loadKey(c.KeyFile, c.KeyEnv)
	if err != nil {
		return nil, err
	}
	opts := []option.RequestOption{
		option.WithAPIKey(key), option.WithMaxRetries(max(0, c.MaxRetries)),
		option.WithHTTPClient(&http.Client{Transport: rt, CheckRedirect: noRedirect}),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	p, ok := prices[c.Model]
	return &Adapter{cfg: c, client: anthropic.NewClient(opts...), price: p, priced: ok}, nil
}

func validEffort(e string) bool {
	switch e {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// maxKeyBytes bounds the key file.
const maxKeyBytes = 4096

// loadKey reads the key from a private file (regular, not accessible by group or others) or
// from the named environment variable. Errors never contain the key.
func loadKey(file, env string) (string, error) {
	if (file == "") == (env == "") {
		return "", errors.New("set exactly one of -api-key-file or -api-key-env")
	}
	if env != "" {
		k := strings.TrimSpace(os.Getenv(env))
		if k == "" {
			return "", fmt.Errorf("environment variable %s is empty", env)
		}
		return k, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("open key file: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat key file: %w", err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("key file %s must be a regular file with mode 0600 (refusing %v)", file, st.Mode().Perm())
	}
	b, err := io.ReadAll(io.LimitReader(f, maxKeyBytes+1))
	if err != nil || len(b) > maxKeyBytes {
		return "", errors.New("key file unreadable or too large")
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", errors.New("key file is empty")
	}
	return k, nil
}

// allowOnly is a transport that refuses any request not sent over https to an allowed host.
type allowTransport struct {
	hosts map[string]bool
	next  http.RoundTripper
}

func allowOnly(hosts map[string]bool, next http.RoundTripper) http.RoundTripper {
	return &allowTransport{hosts: hosts, next: next}
}

func (t *allowTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || !t.hosts[r.URL.Hostname()] {
		return nil, fmt.Errorf("refusing request to %s://%s: only https://%s is allowed", r.URL.Scheme, r.URL.Host, apiHost)
	}
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	return resp, nil
}

func (a *Adapter) model() judgev1.Model {
	return judgev1.Model{Provider: "anthropic", ID: a.cfg.Model}
}

// Judge answers one request. It never returns an error: failures are judge/v1 responses.
func (a *Adapter) Judge(ctx context.Context, req *judgev1.Request) *judgev1.Response {
	if err := req.Validate(); err != nil {
		return judgev1.Failure(req, a.model(), judgev1.ErrCodeBadReply, err.Error())
	}
	maxOut := req.Limits.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = 8000
	}
	in := judgev1.EstimateTokens(req.Prompt.System + req.Prompt.User + string(req.Prompt.ReplySchema))
	if req.DryRun {
		return a.dryRun(req, 700)
	}
	if rem := req.Limits.BudgetRemainingUSD; rem != nil && a.priced && a.price.cost(in, maxOut, 0) > *rem {
		return judgev1.Failure(req, a.model(), judgev1.ErrCodeBudget, "worst-case cost of this request exceeds the remaining budget")
	}
	if req.Limits.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Limits.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	start := time.Now()
	msg, err := a.client.Beta.Messages.New(ctx, a.params(req, maxOut))
	if err != nil {
		return a.failure(req, ctx, err)
	}
	resp := a.response(req, msg)
	resp.LatencyMS = time.Since(start).Milliseconds()
	return resp
}

// dryRun answers without calling the API: usage estimated, no verdict.
func (a *Adapter) dryRun(req *judgev1.Request, out int) *judgev1.Response {
	in := judgev1.EstimateTokens(req.Prompt.System + req.Prompt.User + string(req.Prompt.ReplySchema))
	r := judgev1.Failure(req, a.model(), judgev1.ErrCodeConfig, "")
	r.Error = nil
	r.Usage = judgev1.Usage{InputTokens: in, OutputTokens: out, CostUSD: a.price.cost(in, out, 0), Estimated: true, Billing: judgev1.BillingAPI}
	return r
}

// params builds the request: the core's exact prompt, structured output with the reply schema,
// explicit effort (sampling parameters such as temperature are not accepted by current
// models), a cache breakpoint on the stable system prompt and, by default, the server-side
// refusal fallback.
func (a *Adapter) params(req *judgev1.Request, maxOut int) anthropic.BetaMessageNewParams {
	p := anthropic.BetaMessageNewParams{
		Model:     a.cfg.Model,
		MaxTokens: int64(maxOut),
		System:    []anthropic.BetaTextBlockParam{{Text: req.Prompt.System, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.Prompt.User))},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(a.cfg.Effort),
			Format: anthropic.BetaJSONOutputFormatParam{Schema: req.Prompt.ReplySchema},
		},
	}
	if a.cfg.Fallbacks != "none" {
		p.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		p.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	return p
}

func (a *Adapter) response(req *judgev1.Request, msg *anthropic.BetaMessage) *judgev1.Response {
	m := judgev1.Model{Provider: "anthropic", ID: msg.Model}
	if m.ID == "" {
		m.ID = a.cfg.Model
	}
	u := msg.Usage
	usage := judgev1.Usage{
		InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens), CacheReadInputTokens: int(u.CacheReadInputTokens),
		CacheCreationInputTokens: int(u.CacheCreationInputTokens), Billing: judgev1.BillingAPI,
	}
	if p, ok := prices[m.ID]; ok {
		usage.CostUSD = p.cost(usage.InputTokens+usage.CacheCreationInputTokens, usage.OutputTokens, usage.CacheReadInputTokens)
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		r := judgev1.Failure(req, m, judgev1.ErrCodeRefused, "the model declined the request")
		r.Usage = usage
		return r
	case anthropic.BetaStopReasonMaxTokens:
		r := judgev1.Failure(req, m, judgev1.ErrCodeBadReply, "answer truncated at max_tokens")
		r.Usage = usage
		return r
	default: // end_turn and the rest: read the answer
	}
	var text strings.Builder
	for _, b := range msg.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	ans, err := judgev1.ParseAnswer(req.Task.Type, text.String())
	if err != nil {
		r := judgev1.Failure(req, m, judgev1.ErrCodeBadReply, err.Error())
		r.Usage = usage
		return r
	}
	return judgev1.NewResponse(req, ans, m, usage)
}

// failure maps SDK errors to judge/v1 error codes; messages never include request content
// or credentials.
func (a *Adapter) failure(req *judgev1.Request, ctx context.Context, err error) *judgev1.Response {
	code, msg := judgev1.ErrCodeTransport, "request failed"
	var apiErr *anthropic.Error
	switch {
	case ctx.Err() != nil:
		code, msg = judgev1.ErrCodeTimeout, "no answer before the timeout"
	case errors.As(err, &apiErr):
		msg = fmt.Sprintf("HTTP %d", apiErr.StatusCode)
		switch {
		case apiErr.StatusCode == http.StatusTooManyRequests:
			code = judgev1.ErrCodeRateLimited
		case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden:
			code, msg = judgev1.ErrCodeConfig, msg+": check the API key"
		case apiErr.StatusCode < 500:
			code = judgev1.ErrCodeBadReply
		}
	case strings.Contains(err.Error(), "refusing request"):
		code, msg = judgev1.ErrCodeConfig, "request blocked by the host allow-list"
	}
	return judgev1.Failure(req, a.model(), code, msg)
}

// price is a list price in USD per million tokens.
type price struct{ in, out, cacheRead float64 }

func (p price) cost(in, out, cacheRead int) float64 {
	return (float64(in)*p.in + float64(out)*p.out + float64(cacheRead)*p.cacheRead) / 1e6
}

// prices are first-party list prices (claude-api reference, 2026-11).
var prices = map[string]price{
	"claude-fable-5-2":  {8, 40, 0.20},
	"claude-opus-5-5":   {4, 20, 0.20},
	"claude-opus-5":     {5, 25, 0.50},
	"claude-sonnet-5-5": {2, 10, 0.10},
	"claude-sonnet-5":   {2, 10, 0.20},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01},
}
