package misc

import (
	"reflect"
	"testing"

	"github.com/MIERAI/docs-platform/internal/extract/extracttest"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

func TestExtractors(t *testing.T) {
	cases := []struct {
		name string
		e    plugin.Extractor
		path string
		src  string
		want []string
	}{
		{"make", Make{}, "Makefile", "VAR := 1\nbuild test: deps\n\tgo build\n.PHONY: build\nlint:\n", []string{
			"make_target build [make]", "make_target lint [make]", "make_target test [make]", "program make",
		}},
		{"terraform", Terraform{}, "main.tf", "variable \"region\" {}\noutput \"url\" {}\n", []string{
			"config_key region public", "config_key url",
		}},
		{"javascript", JavaScript{}, "load.js", "const a = __ENV.RATE; const b = process.env['TOKEN']; __ENV.RATE", []string{
			"env_var RATE public", "env_var TOKEN public",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !c.e.Accepts(c.path) {
				t.Fatalf("does not accept %s", c.path)
			}
			if got := extracttest.Symbols(t, c.e, c.path, c.src); !reflect.DeepEqual(got, c.want) {
				t.Errorf("symbols:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}
