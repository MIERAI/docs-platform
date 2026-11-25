package plugin

import (
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/model"
)

type fake struct{ name string }

func (f fake) Info() Info                       { return Info{Name: f.name, Version: "1.0.0", Side: SideCode} }
func (fake) Accepts(string) bool                { return true }
func (fake) Extract(File) (model.Result, error) { return model.Result{}, nil }

func TestRegistry(t *testing.T) {
	if _, err := NewRegistry(fake{"a"}, fake{"a"}); err == nil {
		t.Error("duplicate names must be rejected")
	}
	r, err := NewRegistry(fake{"b"}, fake{"a"}, fake{"c"})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Enabled(map[string]bool{"c": true})
	if len(got) != 2 || got[0].Info().Name != "a" || got[1].Info().Name != "b" {
		t.Errorf("Enabled = %v", got)
	}
	if v := Versions(got); v["extract-a"] != "1.0.0" {
		t.Errorf("Versions = %v", v)
	}
}

func TestDecodeExternal(t *testing.T) {
	good := `{"schema_version":"1.0","extractor":{"name":"rust","version":"0.1.0","side":"code"},
	  "symbols":[{"kind":"env_var","name":"RUST_LOG","path":"src/main.rs","public":true,"confidence":"high","source":"rust"}],
	  "references":[]}`
	out, err := DecodeExternal([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Symbols) != 1 || out.Symbols[0].Confidence != model.ConfHigh {
		t.Errorf("decoded = %+v", out)
	}
	bad := strings.Replace(good, `"high"`, `"certain"`, 1)
	if _, err := DecodeExternal([]byte(bad)); err == nil {
		t.Error("contract violation must be rejected")
	}
}

func FuzzDecodeExternal(f *testing.F) {
	f.Add([]byte(`{"schema_version":"1.0","extractor":{"name":"x","version":"1.0.0","side":"docs"},"symbols":[],"references":[]}`))
	f.Add([]byte(`{"symbols":[{"confidence":7}]}`))
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = DecodeExternal(b)
	})
}
