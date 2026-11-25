package impact

import (
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/repo"
)

func TestMarkdownSections(t *testing.T) {
	doc := strings.Split("intro\n# Title\ntext\n```sh\n# not a heading\n```\n## Sub ##\nmore\n", "\n")
	secs := MarkdownSections("README.md", doc)
	want := []struct {
		heading  string
		from, to int
	}{{"", 1, 1}, {"Title", 2, 6}, {"Sub", 7, 9}}
	if len(secs) != len(want) {
		t.Fatalf("sections %+v", secs)
	}
	for i, w := range want {
		if secs[i].Heading != w.heading || secs[i].From != w.from || secs[i].To != w.to {
			t.Errorf("section %d = %+v, want %+v", i, secs[i], w)
		}
	}
}

func TestCommentSections(t *testing.T) {
	secs := CommentSections("a.go", []int{1, 2, 3, 7, 8}, map[int]string{1: "Package a does x.", 7: "Run runs."})
	if len(secs) != 2 || secs[0].To != 3 || secs[1].From != 7 || secs[1].Heading != "Run runs." {
		t.Fatalf("%+v", secs)
	}
}

func TestDerive(t *testing.T) {
	secs := []Section{
		{Path: "README.md", Heading: "Flags", From: 1, To: 10},
		{Path: "README.md", Heading: "CI", From: 11, To: 20},
		{Path: "docs/ops.md", Heading: "Ops", From: 1, To: 30},
		{Path: "README.md", Heading: "Unrelated", From: 21, To: 30},
	}
	refs := []model.Reference{
		{Kind: model.KindCLIFlag, Text: "--port", Path: "README.md", Line: 3},
		{Kind: model.KindFilePath, Text: "verify.yml", Path: "README.md", Line: 12},
		{Kind: model.KindFilePath, Text: "../scripts", Path: "docs/ops.md", Line: 4},
		{Kind: model.KindTerm, Text: "flag", Path: "README.md", Line: 22},       // plain word: ignored
		{Kind: model.KindEnvVar, Text: "APP_HOME", Path: "README.md", Line: 23}, // not changed
	}
	diffs := []repo.FileDiff{
		{Path: "cmd/main.go", OldPath: "cmd/main.go", Removed: []repo.DiffLine{{N: 10, Text: `port := flag.Int("port", 8080, "")`}}},
		{Path: ".github/workflows/verify.yml", OldPath: ".github/workflows/verify.yml", Added: []repo.DiffLine{{N: 3, Text: "  core:"}}},
		{Path: "scripts/x.sh", Added: []repo.DiffLine{{N: 1, Text: "echo"}}},
		{Path: "README.md", OldPath: "README.md", Added: []repo.DiffLine{{N: 15, Text: "new CI text with flag"}}},
	}
	got := Derive(Attach(secs, refs), diffs)
	type row struct{ path, heading, status, reason string }
	want := []row{
		{"README.md", "Flags", SectionReread, "identifier --port cmd/main.go:10"},
		{"README.md", "CI", SectionUpdated, "path verify.yml .github/workflows/verify.yml"},
		{"docs/ops.md", "Ops", SectionReread, "path ../scripts scripts/x.sh"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		g := got[i]
		r := g.Reasons[0]
		if g.Path != w.path || g.Heading != w.heading || g.Status != w.status || r.Kind+" "+r.Subject+" "+r.Changed != w.reason {
			t.Errorf("row %d = %+v %+v, want %+v", i, g.Section, g.Reasons, w)
		}
	}
}

func TestIdentLike(t *testing.T) {
	tests := []struct {
		k    model.Kind
		s    string
		want bool
	}{
		{model.KindTerm, "flag", false},
		{model.KindTerm, "Window", false},
		{model.KindTerm, "NewOllama", true},
		{model.KindTerm, "max_requests", true},
		{model.KindEnvVar, "HOME", true},
		{model.KindCLIFlag, "-v", false},
	}
	for _, tt := range tests {
		if got := identLike(tt.k, tt.s); got != tt.want {
			t.Errorf("identLike(%s, %q) = %v", tt.k, tt.s, got)
		}
	}
}
