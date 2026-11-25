package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/internal/model"
)

// ExternalTimeout bounds one external extractor run.
const ExternalTimeout = 5 * time.Minute

// maxExternalOutput bounds what an external extractor may print.
const maxExternalOutput = 64 << 20

// ExternalOutput is docs-platform/extract/v1 as printed by an external extractor.
type ExternalOutput struct {
	SchemaVersion string `json:"schema_version"`
	Extractor     struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Side    Side   `json:"side"`
	} `json:"extractor"`
	model.Result
}

// RunExternal executes an out-of-process extractor: argv is run directly (no shell) with the
// repository root appended, a minimal environment, and the repo as working directory. Its
// stdout must be a valid extract/v1 document.
func RunExternal(ctx context.Context, argv []string, root string) (*ExternalOutput, error) {
	if len(argv) == 0 {
		return nil, errors.New("external extractor: empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, ExternalTimeout)
	defer cancel()
	args := append(append([]string{}, argv[1:]...), root)
	cmd := exec.CommandContext(ctx, argv[0], args...) //nolint:gosec // operator-configured absolute path, argv vector, no shell
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	var out, errb bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, n: maxExternalOutput}
	cmd.Stderr = &limitedWriter{w: &errb, n: 64 << 10}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("external extractor %s: %w", argv[0], err)
	}
	return DecodeExternal(out.Bytes())
}

// DecodeExternal validates and decodes extract/v1 JSON.
func DecodeExternal(b []byte) (*ExternalOutput, error) {
	sch, err := jsonschema.Compile(contracts.Extract)
	if err != nil {
		return nil, fmt.Errorf("extract schema: %w", err)
	}
	if errs := sch.ValidateJSON(b); len(errs) > 0 {
		return nil, fmt.Errorf("external extractor output violates extract/v1: %v", errs[:min(5, len(errs))])
	}
	var o ExternalOutput
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("decode extract/v1: %w", err)
	}
	return &o, nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > l.n {
		return 0, errors.New("output limit exceeded")
	}
	l.n -= len(p)
	n, err := l.w.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer: %w", err)
	}
	return n, nil
}
