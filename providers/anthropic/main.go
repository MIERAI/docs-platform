// Command docsp-judge-anthropic is the docs-platform judge/v1 provider for the Anthropic
// Messages API. docs-platform starts it (instance config `provider: anthropic`) and talks to
// it over stdin/stdout: one judge/v1 request per line in, one judge/v1 response per line out.
// It lives in its own module so the core keeps a single third-party dependency; any other
// model or runtime can replace it by configuration (provider exec or claude-code).
//
//	docsp-judge-anthropic -model ID (-api-key-file FILE | -api-key-env NAME)
//	                      [-effort low|medium|high|xhigh|max] [-max-retries N] [-fallbacks default|none]
//
// The API key is read once from a mode-0600 file (anything else is refused) or from the named
// environment variable; it is never logged, echoed or written anywhere. HTTP goes only to
// https://api.anthropic.com (allow-listed in the transport).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/MIERAI/docs-platform/judgev1"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "docsp-judge-anthropic:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("docsp-judge-anthropic", flag.ContinueOnError)
	var c Config
	fs.StringVar(&c.Model, "model", "", "model id (from instance configuration)")
	fs.StringVar(&c.Effort, "effort", "medium", "output_config.effort")
	fs.StringVar(&c.KeyFile, "api-key-file", "", "file holding the API key (mode 0600)")
	fs.StringVar(&c.KeyEnv, "api-key-env", "", "environment variable holding the API key")
	fs.IntVar(&c.MaxRetries, "max-retries", 2, "SDK retries on 408/409/429/5xx (honours retry-after)")
	fs.StringVar(&c.Fallbacks, "fallbacks", "default", "server-side refusal fallback: default or none")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("flags: %w", err)
	}
	a, err := New(c)
	if err != nil {
		return err
	}
	return serve(ctx, a, in, out)
}

// serve answers requests line by line until stdin closes.
func serve(ctx context.Context, a *Adapter, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req judgev1.Request
		var resp *judgev1.Response
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			resp = judgev1.Failure(&judgev1.Request{ID: "invalid", Prompt: judgev1.Prompt{Hash: "0000000000000000"}}, a.model(), judgev1.ErrCodeBadReply, "request is not judge/v1 JSON")
		} else {
			resp = a.Judge(ctx, &req)
		}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}
