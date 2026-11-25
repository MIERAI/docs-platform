package scan

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
)

type panicky struct{}

func (panicky) Info() plugin.Info {
	return plugin.Info{Name: "panicky", Version: "0.0.1", Side: plugin.SideCode}
}
func (panicky) Accepts(p string) bool                     { return strings.HasSuffix(p, ".bad") }
func (panicky) Extract(plugin.File) (model.Result, error) { panic("boom") }

type failing struct{}

func (failing) Info() plugin.Info {
	return plugin.Info{Name: "failing", Version: "0.0.1", Side: plugin.SideCode}
}
func (failing) Accepts(p string) bool { return strings.HasSuffix(p, ".err") }
func (failing) Extract(plugin.File) (model.Result, error) {
	return model.Result{}, errors.New("cannot parse")
}

func TestRunIsolatesPluginFailures(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"a.bad": "x", "b.err": "y", "c.txt": "TOKEN_ONE --flag-two", "d.md": "# doc"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := repo.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv := Run(r, []plugin.Extractor{panicky{}, failing{}}, Options{Docs: []string{"*.md"}})
	if len(inv.Gaps) != 2 || inv.Gaps[0].Path != "a.bad" || !strings.Contains(inv.Gaps[0].Reason, "panicked") {
		t.Errorf("gaps = %+v", inv.Gaps)
	}
	if !inv.CodeTokens["TOKEN_ONE"] || !inv.CodeTokens["--flag-two"] || !inv.CodeTokens["flag-two"] {
		t.Errorf("code tokens missing: %v", inv.CodeTokens)
	}
	if !reflect.DeepEqual(inv.DocFiles, []string{"d.md"}) {
		t.Errorf("doc files = %v", inv.DocFiles)
	}
}

func TestTokens(t *testing.T) {
	got := Tokens([]byte(`os.Getenv("A_B") "--x-y" vars.FACTORY_GEN path/to/f.go.`))
	want := []string{"os.Getenv", "os", "Getenv", "A_B", "--x-y", "x-y", "vars.FACTORY_GEN", "vars", "FACTORY_GEN", "path/to/f.go", "path", "to", "f", "go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tokens = %q\nwant %q", got, want)
	}
}
