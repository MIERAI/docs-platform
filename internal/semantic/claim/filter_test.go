package claim

import "testing"

func TestFilterSkip(t *testing.T) {
	f := DefaultFilter()
	tests := []struct {
		name, path, text, decl string
		comment                bool
		want                   string
	}{
		// historical sentences
		{"previously", "README.md", "The cache previously lived in `/var/cache/x`.", "", false, SkipHistorical},
		{"used to", "README.md", "The service used to listen on port 8080.", "", false, SkipHistorical},
		{"was deprecated", "README.md", "The `-v` flag was deprecated in favour of `-verbose`.", "", false, SkipHistorical},
		{"before vX", "README.md", "Before v1.4 the default timeout was 30s.", "", false, SkipHistorical},
		{"before v2", "README.md", "Before v2 the flag was `-x`.", "", false, SkipHistorical},
		{"prior to release 3", "README.md", "Prior to release 3 jobs ran hourly.", "", false, SkipHistorical},
		{"a limit is not history", "README.md", "Retries up to 3.5 times before 10 seconds pass.", "", false, ""},
		{"zh 以前", "README.md", "以前默认端口是 8080。", "", false, SkipHistorical},
		{"zh 曾经", "README.md", "这个服务曾经部署在 host-a 上。", "", false, SkipHistorical},
		{"zh 已废弃", "README.md", "`-old` 参数已废弃。", "", false, SkipHistorical},
		{"ja 以前は", "README.md", "以前は 8080 番ポートでした。", "", false, SkipHistorical},
		{"ja 廃止された", "README.md", "`legacy` モードは廃止された。", "", false, SkipHistorical},
		{"present tense stays", "README.md", "The service listens on port 8080.", "", false, ""},
		{"is deprecated stays (a present state)", "README.md", "The `-v` flag is deprecated.", "", false, ""},
		{"retention 7 天以前 stays", "README.md", "删除 7 天以前的日志。", "", false, ""},
		{"word inside a code span stays", "README.md", "Run `make previously-built` to rebuild.", "", false, ""},
		// changelog-type documents
		{"CHANGELOG", "CHANGELOG.md", "The default port is 8080.", "", false, SkipChangelog},
		{"nested changelog", "docs/api/Changelog.md", "The default port is 8080.", "", false, SkipChangelog},
		{"HISTORY", "pkg/HISTORY.rst", "The default port is 8080.", "", false, SkipChangelog},
		{"release notes", "RELEASE_NOTES.md", "The default port is 8080.", "", false, SkipChangelog},
		{"docs releases dir", "docs/releases/v2.md", "The default port is 8080.", "", false, SkipChangelog},
		{"code named history is not a changelog", "internal/history/history.go", "The window is 30 days.", "func Open() {", true, ""},
		// test files
		{"comment in a Go test", "internal/x/x_test.go", "The fixture has 3 repos.", "func TestX(t *testing.T) {", true, SkipTestFile},
		{"comment in a JS spec", "web/app.spec.ts", "Retries 3 times.", "", true, SkipTestFile},
		{"doc naming only test files", "README.md", "`internal/x/x_test.go` covers 3 cases.", "", false, SkipTestFile},
		{"doc naming test and code stays", "README.md", "`internal/x/x_test.go` and `internal/x/x.go` cover 3 cases.", "", false, ""},
		// private symbols
		{"comment on an unexported Go func", "internal/x/x.go", "Retries 3 times.", "func retry(n int) error {", true, SkipPrivate},
		{"comment on an unexported method", "internal/x/x.go", "Retries 3 times.", "func (r *Runner) scan(ctx context.Context) {", true, SkipPrivate},
		{"comment on an exported Go func stays", "internal/x/x.go", "Retries 3 times.", "func Retry(n int) error {", true, ""},
		{"comment on a Python private", "x/y.py", "Retries 3 times.", "    def _retry(self):", true, SkipPrivate},
		{"comment on a Kotlin private", "X.kt", "Retries 3 times.", "    private fun retry() {", true, SkipPrivate},
		{"doc naming a private Python name", "README.md", "`_retry` waits 3 seconds.", "", false, SkipPrivate},
		{"doc naming an unexported method", "README.md", "`(*Runner).scan` caches 30 entries.", "", false, SkipPrivate},
		{"doc naming pkg.func()", "README.md", "`ledger.load()` reads at most 1 MiB.", "", false, SkipPrivate},
		{"dunder is public", "README.md", "`__init__` takes 2 arguments.", "", false, ""},
		{"config file is not a symbol", "README.md", "`config.yaml` sets port 8080.", "", false, ""},
		{"exported Go name stays", "README.md", "`ledger.Load` reads at most 1 MiB.", "", false, ""},
	}
	for _, tt := range tests {
		c := fromText(tt.text, spansOf(tt.text))
		c.Path, c.Comment = tt.path, tt.comment
		if got := f.Skip(&c, tt.decl); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	if (Filter{}).Skip(&Claim{Path: "CHANGELOG.md", Text: "previously 3"}, "") != "" {
		t.Error("the zero filter skips nothing")
	}
	if f.Text("CHANGELOG.md", "x is 3") != SkipChangelog || f.Text("README.md", "Port used to be 80.") != SkipHistorical {
		t.Error("Text")
	}
}

func TestMatchAny(t *testing.T) {
	for _, tt := range []struct {
		pat, p string
		want   bool
	}{
		{"changelog*", "CHANGELOG.md", true},
		{"changelog*", "a/b/changelog-2024.md", true},
		{"docs/**/changelog*", "docs/changelog.md", true},
		{"docs/**/changelog*", "docs/a/b/changelog.md", true},
		{"docs/**/changelog*", "site/changelog.md", false},
		{"docs/*.md", "docs/a/b.md", false},
	} {
		if got := MatchAny([]string{tt.pat}, tt.p); got != tt.want {
			t.Errorf("%s %s: %v", tt.pat, tt.p, got)
		}
	}
}
