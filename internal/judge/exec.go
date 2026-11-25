package judge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/MIERAI/docs-platform/judgev1"
)

// maxExecLine bounds one response line of an exec provider.
const maxExecLine = 4 << 20

// ExecConfig configures an out-of-process judge/v1 provider.
type ExecConfig struct {
	Name, Model    string
	Command        []string // absolute program path + fixed arguments; no shell
	PassEnv        []string
	Cloud          bool
	Billing        string
	Timeout        time.Duration
	MaxConcurrency int
	// Modes the program supports (default snippet).
	Modes []string
}

// Exec runs a pool of long-lived provider processes. Each receives one judge/v1 request per
// line on stdin and answers with one judge/v1 response per line on stdout. A process that
// times out, writes garbage or exits is killed and replaced on the next request.
type Exec struct {
	cfg  ExecConfig
	idle chan *proc
	mu   sync.Mutex
	all  []*proc
}

type proc struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	dead bool
}

// NewExec validates cfg. Processes start lazily.
func NewExec(cfg ExecConfig) (*Exec, error) {
	if len(cfg.Command) == 0 || !filepath.IsAbs(cfg.Command[0]) {
		return nil, errors.New("exec provider: command must start with an absolute program path")
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTaskTimeout
	}
	e := &Exec{cfg: cfg, idle: make(chan *proc, cfg.MaxConcurrency)}
	for range cfg.MaxConcurrency {
		e.idle <- nil
	}
	return e, nil
}

// Name implements Provider.
func (e *Exec) Name() string { return e.cfg.Name }

// Model implements Provider.
func (e *Exec) Model() string { return e.cfg.Model }

// Cloud implements Provider.
func (e *Exec) Cloud() bool { return e.cfg.Cloud }

// Billing implements Provider.
func (e *Exec) Billing() string { return e.cfg.Billing }

// Modes implements Provider.
func (e *Exec) Modes() []string {
	if len(e.cfg.Modes) == 0 {
		return snippetOnly
	}
	return e.cfg.Modes
}

// Judge implements Provider.
func (e *Exec) Judge(ctx context.Context, req *judgev1.Request) (*judgev1.Response, error) {
	var p *proc
	select {
	case p = <-e.idle:
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: %w", e.cfg.Name, ctx.Err())
	}
	if p == nil || p.dead {
		np, err := e.start(ctx)
		if err != nil {
			e.idle <- nil
			return nil, err
		}
		p = np
	}
	if req.Model == "" {
		req.Model = e.cfg.Model
	}
	if req.Limits.TimeoutSeconds == 0 {
		req.Limits.TimeoutSeconds = int(e.cfg.Timeout.Seconds())
	}
	resp, err := e.roundTrip(ctx, p, req)
	if err != nil {
		p.kill()
	}
	e.idle <- p
	return resp, err
}

func (e *Exec) start(ctx context.Context) (*proc, error) {
	argv := e.cfg.Command
	// The process outlives this request: it is bound to the pool, not to ctx; Close ends it.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...) //nolint:gosec // operator-configured absolute path, argv vector, no shell
	cmd.Env = childEnv(e.cfg.PassEnv)
	cmd.Dir = filepath.Dir(argv[0])
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stdin: %w", e.cfg.Name, err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stdout: %w", e.cfg.Name, err)
	}
	cmd.Stderr = io.Discard // providers must not need their stderr read; it may echo request data
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: start: %w", e.cfg.Name, err)
	}
	p := &proc{cmd: cmd, in: in, out: bufio.NewReaderSize(out, maxExecLine)}
	e.mu.Lock()
	e.all = append(e.all, p)
	e.mu.Unlock()
	return p, nil
}

func (e *Exec) roundTrip(ctx context.Context, p *proc, req *judgev1.Request) (*judgev1.Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%s: encode: %w", e.cfg.Name, err)
	}
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("%s: write request: %w", e.cfg.Name, err)
	}
	deadline := time.NewTimer(e.cfg.Timeout)
	defer deadline.Stop()
	for {
		l, err := e.readLine(ctx, p, deadline.C)
		if err != nil {
			return nil, err
		}
		var peek struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(l, &peek)
		if peek.Type != "tool_call" {
			return DecodeResponse(l)
		}
		if err := e.answerTool(ctx, p, req, l); err != nil {
			return nil, err
		}
	}
}

// answerTool executes one tool call of an agentic request (tools come from ctx; without them
// every call fails) and writes the result to the provider.
func (e *Exec) answerTool(ctx context.Context, p *proc, req *judgev1.Request, l []byte) error {
	var c judgev1.ToolCall
	res := judgev1.ToolResult{Type: "tool_result", ID: req.ID, Error: "not a judge-tools/v1 tool_call"}
	tools := ToolsFrom(ctx)
	switch {
	case json.Unmarshal(l, &c) != nil || c.ID != req.ID:
	case req.Mode != judgev1.ModeAgentic || tools == nil:
		res.Call, res.Error = c.Call, "tools are only available in agentic mode"
	default:
		res = tools.Call(ctx, c)
	}
	b, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("%s: encode tool result: %w", e.cfg.Name, err)
	}
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("%s: write tool result: %w", e.cfg.Name, err)
	}
	return nil
}

func (e *Exec) readLine(ctx context.Context, p *proc, deadline <-chan time.Time) ([]byte, error) {
	type line struct {
		b   []byte
		err error
	}
	ch := make(chan line, 1)
	go func() {
		l, err := p.out.ReadSlice('\n')
		ch <- line{append([]byte{}, l...), err}
	}()
	select {
	case l := <-ch:
		if l.err != nil {
			return nil, fmt.Errorf("%s: read response: %w", e.cfg.Name, l.err)
		}
		return l.b, nil
	case <-deadline:
		return nil, fmt.Errorf("%s: no response within %s", e.cfg.Name, e.cfg.Timeout)
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: %w", e.cfg.Name, ctx.Err())
	}
}

// DecodeResponse decodes one judge/v1 response line (the Guard validates it against the
// schema afterwards).
func DecodeResponse(b []byte) (*judgev1.Response, error) {
	var r judgev1.Response
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("decode judge/v1 response: %w", err)
	}
	return &r, nil
}

func (p *proc) kill() {
	if p.dead {
		return
	}
	p.dead = true
	_ = p.in.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
}

// Close ends every process: stdin is closed (a well-behaved provider exits), then it is
// killed after a grace period.
func (e *Exec) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.all {
		if p.dead {
			continue
		}
		_ = p.in.Close()
		done := make(chan struct{})
		go func() { _ = p.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = p.cmd.Process.Kill()
			<-done
		}
		p.dead = true
	}
	return nil
}
