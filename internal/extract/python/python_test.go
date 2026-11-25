package python

import (
	"reflect"
	"testing"

	"github.com/MIERAI/docs-platform/internal/extract/extracttest"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"argparse", `import argparse, os
p = argparse.ArgumentParser(prog="mytool")
p.add_argument("--dry-run", "-n", action="store_true")
p.add_argument('target')
sub = p.add_subparsers()
sub.add_parser("sync")
x = os.environ["DB_URL"]; y = os.environ.get('LOG_LEVEL', 'info'); z = os.getenv("HOME_X")
`, []string{
			"cli_command sync [mytool] public", "cli_flag --dry-run [mytool] public", "cli_flag -n [mytool]",
			"env_var DB_URL [mytool] public", "env_var HOME_X [mytool] public", "env_var LOG_LEVEL [mytool] public",
			"program mytool public",
		}},
		{"click and metrics", `import click
from prometheus_client import Counter, Histogram
JOBS = Counter("jobs_done", "jobs")
LAT = Histogram('job_seconds', 'latency')
@click.command("run")
@click.option("--force")
def run(force): pass
if __name__ == "__main__":
    run()
`, []string{
			"cli_command run [tool] public", "cli_flag --force [tool] public", "metric job_seconds public",
			"metric jobs_done public", "program tool public",
		}},
		{"lower-case env is ignored", `import os
os.environ.get("not_an_env")`, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extracttest.Symbols(t, Extractor{}, "scripts/tool.py", c.src)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("symbols:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestAccepts(t *testing.T) {
	for p, want := range map[string]bool{"a.py": true, "tests/x.py": false, "test_a.py": false, "a_test.py": false, "a.pyc": false} {
		if (Extractor{}).Accepts(p) != want {
			t.Errorf("Accepts(%q) != %v", p, want)
		}
	}
}
