package rulejudge

import (
	"strconv"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/agentic"
)

const mainGo = `package main

import (
	"flag"
	"net/http"
	"os"
	"time"
)

// DefaultWorkers is the size of the worker pool.
const DefaultWorkers = 8

const internalLimit = 3

func main() {
	flag.Int("workers", DefaultWorkers, "number of parallel workers")
	flag.String("format", "json", "output format")
	flag.Duration("timeout", 90*time.Second, "request timeout")
	flag.Bool("verbose", false, "verbose output")
	flag.Int("max-questions", 0, "claims put to the judge (default 150)")
	flag.Int("retries", 0, "retries")
	flag.String("out", "", "output file")
	level := os.Getenv("APP_LEVEL")
	if level == "" {
		level = "info"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", getItem)
	mux.HandleFunc("POST /items", createItem)
	mux.HandleFunc("/healthz", health)
}
`

const otherGo = `package other

import "flag"

func register(fs *flag.FlagSet) {
	fs.String("format", "text", "output format")
	fs.Int("workers", 8, "number of parallel workers")
}
`

const configGo = `package config

import "time"

type Config struct {
	// Retention is how long reports are kept.
	Retention time.Duration ` + "`yaml:\"retention\"`" + `
}

func NewConfig() Config { return Config{Retention: 72 * time.Hour} }
`

func testStage(t *testing.T) *Stage {
	t.Helper()
	files := map[string]string{"cmd/app/main.go": mainGo, "internal/other/other.go": otherGo, "internal/config/config.go": configGo}
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	return Load(func(p string) ([]byte, error) { return []byte(files[p]), nil }, paths)
}

func TestDecide(t *testing.T) {
	s := testStage(t)
	cases := []struct {
		name, doc, line string
		want            string // "" = undecided
		draft           string
	}{
		{"flag default holds", "README.md", "| `--workers N` | number of parallel workers (default 8) |", agentic.Confirmed, ""},
		{
			"flag default wrong", "README.md", "| `-workers N` | number of parallel workers (default 4) |", agentic.Contradicted,
			"| `-workers N` | number of parallel workers (default 8) |",
		},
		{"defaults to", "docs/guide.md", "The `-timeout` flag defaults to 30s.", agentic.Contradicted, "The `-timeout` flag defaults to 1m30s."},
		{"duration words", "docs/guide.md", "`-timeout` defaults to 90 seconds.", agentic.Confirmed, ""},
		{"duration words wrong keeps style", "docs/guide.md", "`-timeout` defaults to 60 seconds.", agentic.Contradicted, "`-timeout` defaults to 90 seconds."},
		{"bool", "README.md", "`-verbose` (default `true`)", agentic.Contradicted, "`-verbose` (default `false`)"},
		{"usage default resolves the zero sentinel", "README.md", "| `--max-questions N` | claims put to the judge (default 150) |", agentic.Confirmed, ""},
		{
			"usage default contradicted", "README.md", "| `--max-questions N` | claims put to the judge (default 20) |", agentic.Contradicted,
			"| `--max-questions N` | claims put to the judge (default 150) |",
		},
		{"exported const is", "docs/design.md", "`DefaultWorkers` is 6.", agentic.Contradicted, "`DefaultWorkers` is 8."},
		{"exported const holds", "docs/design.md", "`DefaultWorkers` = 8", agentic.Confirmed, ""},
		{"env default", "README.md", "`APP_LEVEL` defaults to `debug`.", agentic.Contradicted, "`APP_LEVEL` defaults to `info`."},
		{"config field by tag", "README.md", "`retention` defaults to 72h.", agentic.Confirmed, ""},
		{"config field wrong", "README.md", "`retention` defaults to 24h.", agentic.Contradicted, "`retention` defaults to 72h."},
		{"route holds", "README.md", "`GET /items/{itemID}`", agentic.Confirmed, ""},
		{"route method wrong", "README.md", "`PUT /items`", agentic.Contradicted, "`POST /items`"},

		{"flags of two programs disagree", "README.md", "`-format` defaults to `json`.", "", ""},
		{"same value in two programs agrees", "README.md", "`-workers` defaults to 8.", agentic.Confirmed, ""},
		{"zero default is a sentinel", "README.md", "`-retries` defaults to 3.", "", ""},
		{"empty string default is a sentinel", "README.md", "`-out` defaults to `report.json`.", "", ""},
		{"word is not a value", "README.md", "`-out` (default: the directory name)", "", ""},
		{"bare string is not a value", "README.md", "`APP_LEVEL` defaults to info.", "", ""},
		{"line says more", "README.md", "`-workers` defaults to 8, one per CPU core.", "", ""},
		{"qualifier", "README.md", "`-workers` defaults to 4 in CI.", "", ""},
		{"history document", "CHANGELOG.md", "`-workers` defaults to 4.", "", ""},
		{"two subjects", "README.md", "`-workers` and `-timeout` default to 8.", "", ""},
		{"unexported const", "README.md", "`internalLimit` is 5.", "", ""},
		{"unknown subject", "README.md", "`-parallel` defaults to 4.", "", ""},
		{"value then words", "README.md", "`-workers` defaults to 4 8.", "", ""},
		{"route only for path without method", "README.md", "`DELETE /healthz`", agentic.Confirmed, ""},
		{"unknown route", "README.md", "`GET /nothing`", "", ""},
		{"route with prose", "README.md", "`GET /items/{id}` returns one item as JSON.", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, ok := s.Decide(c.doc, c.line)
			got := ""
			if ok {
				got = d.Category
			}
			if got != c.want {
				t.Fatalf("Decide(%q) = %q (%+v), want %q", c.line, got, d, c.want)
			}
			if ok && d.Draft != c.draft {
				t.Errorf("draft %q, want %q", d.Draft, c.draft)
			}
			if ok && (len(d.Quotes) == 0 || d.Reason == "") {
				t.Errorf("decision without quotes or reason: %+v", d)
			}
		})
	}
}

func TestQuotesAreVerbatimSourceLines(t *testing.T) {
	s := testStage(t)
	d, ok := s.Decide("README.md", "`-workers` defaults to 8.")
	if !ok {
		t.Fatal("undecided")
	}
	lines := map[string][]string{"cmd/app/main.go": strings.Split(mainGo, "\n"), "internal/other/other.go": strings.Split(otherGo, "\n")}
	want := map[string]bool{
		"cmd/app/main.go:16": true, "cmd/app/main.go:11": true, "internal/other/other.go:7": true,
	}
	for _, q := range d.Quotes {
		key := q.Path + ":" + strconv.Itoa(q.Line)
		if !want[key] {
			t.Errorf("unexpected quote %s", key)
		}
		if got := strings.TrimSpace(lines[q.Path][q.Line-1]); got != q.Quote {
			t.Errorf("quote %s = %q, source line %q", key, q.Quote, got)
		}
	}
	if len(d.Quotes) != len(want) {
		t.Errorf("quotes %+v, want %v", d.Quotes, want)
	}
}

func TestResultIsALedgerReadyRulesVerdict(t *testing.T) {
	s := testStage(t)
	d, ok := s.Decide("README.md", "`-timeout` defaults to 30s.")
	if !ok {
		t.Fatal("undecided")
	}
	lr := d.Result("README.md", 7)
	if !agentic.IsRules(lr.Model) || lr.Model != Model || lr.PromptHash != PromptHash() || lr.Parse != agentic.ParseRules {
		t.Errorf("not marked as a rules verdict: %+v", lr)
	}
	if lr.Category != agentic.Contradicted || lr.Verdict != "yes" || lr.EvidenceOK != 1 || !lr.Facts[0].Valid {
		t.Errorf("result %+v", lr)
	}
	a := d.Answer()
	if a.Category != agentic.Contradicted || len(a.Answer.Facts) != 1 || a.Answer.Facts[0].Evidence[0].Kind != agentic.KindQuote {
		t.Errorf("answer %+v", a)
	}
}

func TestPromptHashStable(t *testing.T) {
	if a, b := PromptHash(), PromptHash(); len(a) != 16 || a != b {
		t.Fatal(PromptHash())
	}
}

func TestRenderDuration(t *testing.T) {
	for _, c := range []struct {
		secs float64
		old  string
		want string
	}{
		{90, "30s", "1m30s"},
		{60, "30s", "1m"},
		{3600, "1m", "1h"},
		{5400, "1m", "1h30m"},
		{0.5, "1s", "500ms"},
		{120, "30 seconds", "120 seconds"},
		{60, "2 minutes", "1 minute"},
		{90, "2 minutes", ""},
	} {
		if got := renderDuration(c.secs, c.old); got != c.want {
			t.Errorf("renderDuration(%v, %q) = %q, want %q", c.secs, c.old, got, c.want)
		}
	}
}

func FuzzDecide(f *testing.F) {
	s := Load(func(string) ([]byte, error) { return []byte(mainGo), nil }, []string{"cmd/app/main.go"})
	f.Add("| `--workers N` | number of parallel workers (default 8) |")
	f.Add("`DefaultWorkers` is 6.")
	f.Add("`GET /items/{id}`")
	f.Add("`-timeout` defaults to `")
	f.Fuzz(func(t *testing.T, line string) {
		d, ok := s.Decide("README.md", line)
		if ok && d.Draft != "" && d.Draft == line {
			t.Errorf("draft equals the line: %q", line)
		}
	})
}
