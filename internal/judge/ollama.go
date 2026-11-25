package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/MIERAI/docs-platform/judgev1"
)

// OllamaConfig configures the ollama provider. All values come from instance configuration;
// nothing about the endpoint is built into the product.
type OllamaConfig struct {
	Endpoint       string        // e.g. http://127.0.0.1:11434
	Model          string        // e.g. a small local model
	Timeout        time.Duration // per request
	Retries        int           // extra attempts on transport errors and 5xx
	MaxConcurrency int           // requests in flight (the GPU is usually shared)
	Seed           int
	NumCtx         int
	NumPredict     int
}

// Ollama talks to an Ollama server over HTTP. It refuses to connect to anything but loopback,
// private, CGNAT (tailnet) or link-local addresses, checked on the resolved address at dial
// time, so a misconfigured endpoint cannot send repository content to the internet.
type Ollama struct {
	cfg    OllamaConfig
	base   *url.URL
	client *http.Client
	sem    chan struct{}
}

// Ollama defaults for unset fields.
const (
	DefaultTimeout     = 60 * time.Second
	DefaultConcurrency = 2
	DefaultSeed        = 42
	DefaultNumCtx      = 8192
	DefaultNumPredict  = 256
	maxReplyBytes      = 1 << 20
)

// NewOllama validates cfg and returns a provider.
func NewOllama(cfg OllamaConfig) (*Ollama, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("ollama endpoint %q: want http(s)://host:port", cfg.Endpoint)
	}
	if cfg.Model == "" {
		return nil, errors.New("ollama: model is required")
	}
	fillDefaults(&cfg)
	d := &net.Dialer{Timeout: 10 * time.Second, Control: localOnly}
	tr := &http.Transport{DialContext: d.DialContext, Proxy: nil, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &Ollama{
		cfg: cfg, base: u, sem: make(chan struct{}, cfg.MaxConcurrency),
		client: &http.Client{
			Transport: tr, Timeout: cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func fillDefaults(c *OllamaConfig) {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	def(&c.MaxConcurrency, DefaultConcurrency)
	def(&c.Seed, DefaultSeed)
	def(&c.NumCtx, DefaultNumCtx)
	def(&c.NumPredict, DefaultNumPredict)
	if c.Retries < 0 {
		c.Retries = 0
	}
}

// localOnly rejects connections to public addresses.
func localOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("ollama dial %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !IsLocalIP(ip) {
		return fmt.Errorf("ollama: refusing non-local address %s (repository content must stay on the operator's machines)", host)
	}
	return nil
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// IsLocalIP reports whether ip is loopback, private (RFC 1918 / ULA), CGNAT (e.g. a tailnet)
// or link-local.
func IsLocalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}

// Name implements Provider.
func (*Ollama) Name() string { return "ollama" }

// Model implements Provider.
func (o *Ollama) Model() string { return o.cfg.Model }

// Cloud implements Provider: the dialer only reaches local addresses.
func (*Ollama) Cloud() bool { return false }

// Billing implements Provider.
func (*Ollama) Billing() string { return judgev1.BillingNone }

// Modes implements Provider: snippet, and agentic (native tool calls against the core's
// judge-tools, then a final answer constrained by the reply schema as Ollama's format).
func (*Ollama) Modes() []string { return []string{judgev1.ModeSnippet, judgev1.ModeAgentic} }

// Close implements Provider.
func (o *Ollama) Close() error {
	o.client.CloseIdleConnections()
	return nil
}

type genRequest struct {
	Model   string          `json:"model"`
	System  string          `json:"system,omitempty"`
	Prompt  string          `json:"prompt"`
	Stream  bool            `json:"stream"`
	Think   bool            `json:"think"`
	Format  json.RawMessage `json:"format,omitempty"`
	Options genOptions      `json:"options"`
}

type genOptions struct {
	Temperature float64 `json:"temperature"`
	Seed        int     `json:"seed"`
	NumCtx      int     `json:"num_ctx"`
	NumPredict  int     `json:"num_predict"`
}

type genResponse struct {
	Model           string `json:"model"`
	Response        string `json:"response"`
	Error           string `json:"error"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// Judge implements Provider: deterministic options (temperature 0, fixed seed), thinking off,
// bounded concurrency and retries on transient errors.
func (o *Ollama) Judge(ctx context.Context, req *judgev1.Request) (*judgev1.Response, error) {
	m := judgev1.Model{Provider: o.Name(), ID: o.cfg.Model}
	if req.DryRun {
		return dryRun(req, m, judgev1.BillingNone, nil), nil
	}
	if req.Mode == judgev1.ModeAgentic {
		select {
		case o.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, fmt.Errorf("ollama: %w", ctx.Err())
		}
		defer func() { <-o.sem }()
		return o.judgeAgentic(ctx, req)
	}
	body, err := json.Marshal(genRequest{
		Model: o.cfg.Model, System: req.Prompt.System, Prompt: req.Prompt.User, Format: req.Prompt.ReplySchema,
		Options: genOptions{Seed: o.cfg.Seed, NumCtx: o.cfg.NumCtx, NumPredict: o.cfg.NumPredict},
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	select {
	case o.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("ollama: %w", ctx.Err())
	}
	defer func() { <-o.sem }()
	var last error
	for attempt := 0; attempt <= o.cfg.Retries; attempt++ {
		g, retry, err := o.once(ctx, body)
		if err == nil {
			return o.response(req, g), nil
		}
		if !retry {
			return nil, err
		}
		last = err
		if err := sleep(ctx, time.Duration(attempt+1)*500*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return nil, last
}

func (o *Ollama) response(req *judgev1.Request, g genResponse) *judgev1.Response {
	m := judgev1.Model{Provider: o.Name(), ID: g.Model}
	if m.ID == "" {
		m.ID = o.cfg.Model
	}
	a, err := judgev1.ParseAnswer(req.Task.Type, g.Response)
	if err != nil {
		return judgev1.Failure(req, m, judgev1.ErrCodeBadReply, err.Error())
	}
	return judgev1.NewResponse(req, a, m, judgev1.Usage{
		InputTokens: g.PromptEvalCount, OutputTokens: g.EvalCount, Billing: judgev1.BillingNone,
	})
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("judge: %w", ctx.Err())
	}
}

// once sends one request; retry reports whether a failure is transient.
func (o *Ollama) once(ctx context.Context, body []byte) (genResponse, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base.JoinPath("api", "generate").String(), bytes.NewReader(body))
	if err != nil {
		return genResponse{}, false, fmt.Errorf("ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return genResponse{}, ctx.Err() == nil, fmt.Errorf("ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return genResponse{}, true, fmt.Errorf("ollama read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return genResponse{}, resp.StatusCode >= 500, fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}
	var g genResponse
	if err := json.Unmarshal(b, &g); err != nil {
		return genResponse{}, false, fmt.Errorf("ollama decode: %w", err)
	}
	if g.Error != "" {
		return genResponse{}, false, fmt.Errorf("ollama: %s", g.Error)
	}
	return g, false, nil
}
