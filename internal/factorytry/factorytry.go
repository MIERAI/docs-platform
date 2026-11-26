// Package factorytry runs an API factory's `factory try` (factory.try-request/v1 →
// factory.try-result/v1): "if these files were changed, does the factory still accept the
// project, and which cases pass or fail?". The factory applies the change to a temporary
// copy of a revision, regenerates deterministically, builds and runs its unit tests and its
// acceptance; the caller's checkout is never touched.
//
// docs-platform uses it to turn AI proposals into executable facts: a rule mutant is caught
// or not by the design's examples; a drafted example or rule rewording passes acceptance or
// not. The factory decides; the model only proposes.
package factorytry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Schemas.
const (
	RequestSchema = "factory.try-request/v1"
	ResultSchema  = "factory.try-result/v1"
)

// Statuses of a try.
const (
	StatusOK          = "ok"
	StatusGateFailed  = "gate_failed"
	StatusBuildFailed = "build_failed"
	StatusNeedsAI     = "needs_ai"
	StatusError       = "error"
)

// Case sources. Example and LogicExample are written in the design (executable statements
// of its rules); Auto and Differential are derived by the factory from the design's
// structure; HandTest is hand-written code, not part of the design.
const (
	SourceExample      = "example"
	SourceLogicExample = "logic_example"
	SourceAuto         = "auto"
	SourceDifferential = "differential"
	SourceHandTest     = "hand_test"
)

// File is one changed file: the full new content, or Delete.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Delete  bool   `json:"delete,omitempty"`
}

// Request is factory.try-request/v1.
type Request struct {
	Schema string `json:"schema"`
	Files  []File `json:"files"`
}

// Case is one test or acceptance case.
type Case struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// API is the cases of one API.
type API struct {
	ID    string `json:"id"`
	Cases []Case `json:"cases"`
}

// Count is passed and failed cases.
type Count struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

// Result is factory.try-result/v1.
type Result struct {
	Schema  string `json:"schema"`
	Base    string `json:"base"`
	Status  string `json:"status"`
	Stage   string `json:"stage"`
	Message string `json:"message,omitempty"`
	APIs    []API  `json:"apis"`
	Summary struct {
		Passed   int              `json:"passed"`
		Failed   int              `json:"failed"`
		BySource map[string]Count `json:"by_source"`
	} `json:"summary"`
}

// Failed lists the failing cases of the given sources (all sources when none given).
func (r *Result) Failed(sources ...string) []Case {
	var out []Case
	for _, a := range r.APIs {
		for _, c := range a.Cases {
			if !c.Passed && (len(sources) == 0 || contains(sources, c.Source)) {
				out = append(out, c)
			}
		}
	}
	return out
}

// Ran counts the cases of the given sources that ran (passed or failed).
func (r *Result) Ran(sources ...string) int {
	n := 0
	for _, a := range r.APIs {
		for _, c := range a.Cases {
			if len(sources) == 0 || contains(sources, c.Source) {
				n++
			}
		}
	}
	return n
}

// Design are the sources written in the design: what makes a rule executable.
var Design = []string{SourceExample, SourceLogicExample}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Runner runs a try.
type Runner interface {
	Try(ctx context.Context, files []File, apis []string) (*Result, error)
}

// Command runs `factory try` as a program in the project's checkout: Argv is the factory
// command (for example ["go", "run", "./factory/cmd/factory"]), Dir the checkout, Base the
// revision to try against ("" = HEAD).
type Command struct {
	Argv []string
	Dir  string
	Base string
	// TempDir holds the request and result files ("" = the system's).
	TempDir string
}

// Try implements Runner.
func (c *Command) Try(ctx context.Context, files []File, apis []string) (*Result, error) {
	if len(c.Argv) == 0 {
		return nil, errors.New("factory try: no factory command")
	}
	tmp, err := os.MkdirTemp(c.TempDir, "docsp-try-")
	if err != nil {
		return nil, fmt.Errorf("factory try: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	req, out := filepath.Join(tmp, "request.json"), filepath.Join(tmp, "result.json")
	b, err := json.Marshal(Request{Schema: RequestSchema, Files: files})
	if err != nil {
		return nil, fmt.Errorf("factory try: %w", err)
	}
	if err := os.WriteFile(req, b, 0o600); err != nil {
		return nil, fmt.Errorf("factory try: %w", err)
	}
	args := append(append([]string{}, c.Argv[1:]...), "try", "--changes", req, "--out", out)
	if c.Base != "" {
		args = append(args, "--base", c.Base)
	}
	for _, a := range apis {
		args = append(args, "--api", a)
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], args...) //nolint:gosec // operator-configured factory command, argv vector
	cmd.Dir = c.Dir
	var stderr bytes.Buffer
	cmd.Stderr = &capWriter{w: &stderr, n: 4 << 10}
	runErr := cmd.Run()
	rb, err := os.ReadFile(out)
	if err != nil {
		if runErr == nil {
			runErr = err
		}
		return nil, fmt.Errorf("factory try: no result (%w): %s", runErr, strings.TrimSpace(stderr.String()))
	}
	return Parse(rb)
}

// Parse reads and checks a factory.try-result/v1.
func Parse(b []byte) (*Result, error) {
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("factory try: result: %w", err)
	}
	if r.Schema != ResultSchema {
		return nil, fmt.Errorf("factory try: result schema %q, want %s", r.Schema, ResultSchema)
	}
	if r.Status == "" {
		return nil, errors.New("factory try: result without status")
	}
	return &r, nil
}

type capWriter struct {
	w *bytes.Buffer
	n int
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.n - c.w.Len(); room > 0 {
		c.w.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
