package ai

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
	"sync/atomic"
	"syscall"
	"time"
)

// Question is one small, verifiable question for a judge model. System and Prompt are built by
// the caller; repository content inside them is untrusted data. Schema, when set, is a JSON
// Schema the reply must follow (structured output).
type Question struct {
	System string
	Prompt string
	Schema json.RawMessage
}

// Reply is the raw model answer.
type Reply struct {
	Text  string
	Model string
}

// Asker is a provider that answers small questions. Only local models are built in: repository
// content must never leave the operator's machines.
type Asker interface {
	Name() string
	Model() string
	Ask(ctx context.Context, q Question) (Reply, error)
	// Used reports how many requests were sent (successful or not).
	Used() int
}

// ErrBudget is returned once the per-run request budget is spent.
var ErrBudget = errors.New("AI request budget exhausted")

// OllamaConfig configures the ollama provider. All values come from instance configuration;
// nothing about the endpoint is built into the product.
type OllamaConfig struct {
	Endpoint       string        // e.g. http://127.0.0.1:11434
	Model          string        // e.g. a small local model
	Timeout        time.Duration // per request
	Retries        int           // extra attempts on transport errors and 5xx
	MaxConcurrency int           // requests in flight (the GPU is usually shared)
	MaxRequests    int           // per-run budget
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
	used   atomic.Int64
}

// Defaults for unset OllamaConfig fields.
const (
	DefaultTimeout     = 60 * time.Second
	DefaultConcurrency = 2
	DefaultMaxRequests = 300
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
	def(&c.MaxRequests, DefaultMaxRequests)
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

// Name implements Asker.
func (*Ollama) Name() string { return "ollama" }

// Model implements Asker.
func (o *Ollama) Model() string { return o.cfg.Model }

// Used implements Asker.
func (o *Ollama) Used() int { return int(o.used.Load()) }

// Propose implements Provider: proposals (layer ④b) are not built for local judges yet.
func (*Ollama) Propose(context.Context, Request) ([]Proposal, error) {
	return nil, errors.New("ollama provider answers audit questions only; proposals are not implemented")
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
	Model    string `json:"model"`
	Response string `json:"response"`
	Error    string `json:"error"`
}

// Ask implements Asker: deterministic options (temperature 0, fixed seed), thinking off,
// bounded concurrency, a per-run budget and retries on transient errors.
func (o *Ollama) Ask(ctx context.Context, q Question) (Reply, error) {
	body, err := json.Marshal(genRequest{
		Model: o.cfg.Model, System: q.System, Prompt: q.Prompt, Format: q.Schema,
		Options: genOptions{Seed: o.cfg.Seed, NumCtx: o.cfg.NumCtx, NumPredict: o.cfg.NumPredict},
	})
	if err != nil {
		return Reply{}, fmt.Errorf("encode request: %w", err)
	}
	select {
	case o.sem <- struct{}{}:
	case <-ctx.Done():
		return Reply{}, fmt.Errorf("ollama: %w", ctx.Err())
	}
	defer func() { <-o.sem }()
	var last error
	for attempt := 0; attempt <= o.cfg.Retries; attempt++ {
		if int(o.used.Add(1)) > o.cfg.MaxRequests {
			o.used.Add(-1)
			return Reply{}, ErrBudget
		}
		r, retry, err := o.once(ctx, body)
		if err == nil || !retry {
			return r, err
		}
		last = err
		if err := sleep(ctx, time.Duration(attempt+1)*500*time.Millisecond); err != nil {
			return Reply{}, err
		}
	}
	return Reply{}, last
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ollama: %w", ctx.Err())
	}
}

// once sends one request; retry reports whether a failure is transient.
func (o *Ollama) once(ctx context.Context, body []byte) (Reply, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base.JoinPath("api", "generate").String(), bytes.NewReader(body))
	if err != nil {
		return Reply{}, false, fmt.Errorf("ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return Reply{}, ctx.Err() == nil, fmt.Errorf("ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return Reply{}, true, fmt.Errorf("ollama read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Reply{}, resp.StatusCode >= 500, fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}
	var g genResponse
	if err := json.Unmarshal(b, &g); err != nil {
		return Reply{}, false, fmt.Errorf("ollama decode: %w", err)
	}
	if g.Error != "" {
		return Reply{}, false, fmt.Errorf("ollama: %s", g.Error)
	}
	return Reply{Text: g.Response, Model: g.Model}, false, nil
}
