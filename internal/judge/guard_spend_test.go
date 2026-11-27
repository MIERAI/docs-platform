package judge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/judgev1"
)

type closer struct{}

func (closer) Name() string    { return "fake" }
func (closer) Model() string   { return "m" }
func (closer) Cloud() bool     { return false }
func (closer) Billing() string { return judgev1.BillingNone }
func (closer) Close() error    { return nil }
func (closer) Modes() []string { return nil }
func (closer) Judge(context.Context, *judgev1.Request) (*judgev1.Response, error) {
	return nil, nil //nolint:nilnil // never called
}

func TestSpendLogOncePerGuard(t *testing.T) {
	p := filepath.Join(t.TempDir(), "spend")
	t.Setenv(SpendLogEnv, p)
	g := &Guard{p: closer{}}
	g.st.CostUSD = 0.25
	_ = g.Close()
	_ = g.Close()
	b, err := os.ReadFile(p)
	if err != nil || strings.Count(string(b), "\n") != 1 || !strings.HasSuffix(strings.TrimSpace(string(b)), " 0.250000") {
		t.Fatalf("%q %v", b, err)
	}
}
