package refclass

import (
	"reflect"
	"testing"

	"github.com/MIERAI/docs-platform/internal/model"
)

func TestSpan(t *testing.T) {
	cases := []struct {
		in   string
		want []Candidate
	}{
		{"--dry-run", []Candidate{{Kind: model.KindCLIFlag, Text: "--dry-run", Confidence: model.ConfMedium}}},
		{"--out=x.json", []Candidate{{Kind: model.KindCLIFlag, Text: "--out", Confidence: model.ConfMedium}}},
		{"-v", []Candidate{{Kind: model.KindCLIFlag, Text: "-v", Confidence: model.ConfLow}}},
		{"$API_TOKEN", []Candidate{{Kind: model.KindEnvVar, Text: "API_TOKEN", Confidence: model.ConfHigh}}},
		{"API_TOKEN", []Candidate{{Kind: model.KindEnvVar, Text: "API_TOKEN", Confidence: model.ConfMedium}}},
		{"LEVEL=debug", []Candidate{{Kind: model.KindEnvVar, Text: "LEVEL", Confidence: model.ConfMedium}}},
		{"http_requests_total", []Candidate{{Kind: model.KindMetric, Text: "http_requests_total", Confidence: model.ConfMedium}}},
		{"scripts/run.sh", []Candidate{{Kind: model.KindFilePath, Text: "scripts/run.sh", Confidence: model.ConfMedium}}},
		{"go.mod", []Candidate{{Kind: model.KindFilePath, Text: "go.mod", Confidence: model.ConfMedium}}},
		{"server.port", []Candidate{{Kind: model.KindConfigKey, Text: "server.port", Confidence: model.ConfLow}}},
		{"example.com", nil},
		{"https://x.y/z", nil},
		{"README", nil},
		{"rate(app_errors_total[5m])", []Candidate{{Kind: model.KindMetric, Text: "app_errors_total", Confidence: model.ConfLow}}},
		{"", nil},
	}
	for _, c := range cases {
		if got := Span(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Span(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestCommandLine(t *testing.T) {
	type kt struct {
		Kind    model.Kind
		Text    string
		Program string
	}
	cases := []struct {
		in   string
		want []kt
	}{
		{"docsp check --min-confidence low .", []kt{{model.KindCLICommand, "check", "docsp"}, {model.KindCLIFlag, "--min-confidence", "docsp"}}},
		{"FOO=1 ./scripts/x.sh --fast", []kt{{model.KindEnvVar, "FOO", ""}, {model.KindFilePath, "./scripts/x.sh", ""}, {model.KindCLIFlag, "--fast", "x"}}},
		{"make build test", []kt{{model.KindMakeTarget, "build", "make"}, {model.KindMakeTarget, "test", "make"}}},
		{"sudo env A=b tool run", []kt{{model.KindEnvVar, "A", ""}, {model.KindCLICommand, "run", "tool"}}},
		{"python3 tools/gen.py --all && go run ./cmd/app serve", []kt{{model.KindFilePath, "tools/gen.py", ""}, {model.KindCLIFlag, "--all", "gen"}, {model.KindFilePath, "./cmd/app", ""}, {model.KindCLICommand, "serve", "app"}}},
		{"tool --out report.json run", []kt{{model.KindCLIFlag, "--out", "tool"}, {model.KindCLICommand, "run", "tool"}}},
		{"tool run > out/x.txt # comment --nope", []kt{{model.KindCLICommand, "run", "tool"}}},
		{"cd sub && ./run.sh data/in.txt", []kt{{model.KindFilePath, "sub/run.sh", ""}, {model.KindFilePath, "sub/data/in.txt", ""}}},
		{"echo $HOME_DIR", []kt{{model.KindEnvVar, "HOME_DIR", ""}}},
	}
	for _, c := range cases {
		var got []kt
		for _, x := range CommandLine(c.in, model.ConfMedium) {
			got = append(got, kt{x.Kind, x.Text, x.Program})
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("CommandLine(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`a "b c" 'd e' f\ g`, []string{"a", "b c", "d e", "f g"}},
		{"a&&b||c;d|e", []string{"a", "&&", "b", "||", "c", ";", "d", "|", "e"}},
		{`"unbalanced`, []string{"unbalanced"}},
		{`""`, []string{""}},
	}
	for _, c := range cases {
		if got := Words(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Words(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBlock(t *testing.T) {
	lines := []string{"$ tool run \\", "    --fast", "running...", "$ make lint"}
	var got []string
	for _, l := range Block("console", lines) {
		got = append(got, l.Text)
	}
	want := []string{"run", "--fast", "lint"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Block = %q, want %q", got, want)
	}
	if Block("go", lines) != nil {
		t.Error("non-shell blocks must yield nothing")
	}
}

func TestProsePathsAndQualifier(t *testing.T) {
	text := "see scripts/env.sh, other/repo docs/x.md and notes.md."
	got := ProsePaths(text)
	if len(got) != 3 || got[0].Text != "scripts/env.sh" || got[2].Confidence != model.ConfLow {
		t.Fatalf("ProsePaths = %+v", got)
	}
	if q := Qualifier(text, got[1].Offset); q != "other/repo" {
		t.Errorf("Qualifier = %q, want other/repo", q)
	}
}

func FuzzCommandLine(f *testing.F) {
	for _, s := range []string{"a 'b", "FOO=1 make x", "cd .. && ./x.sh --y=$Z", "\\", "go run ./ --", ">"} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, s string) {
		_ = CommandLine(s, model.ConfMedium)
		_ = Span(s)
		_ = ProsePaths(s)
		_ = Qualifier(s, len(s)/2)
	})
}
