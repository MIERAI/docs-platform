package golang

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// render writes a fact as "kind name=value@line" plus its method, aliases, anchoring and the
// value's declaration line when it is elsewhere.
func render(f Fact) string {
	s := fmt.Sprintf("%s %s", f.Kind, f.Name)
	if f.Method != "" {
		s = fmt.Sprintf("%s %s %s", f.Kind, f.Method, f.Name)
	}
	switch f.Value.Kind {
	case ValString:
		s += "=" + strconv.Quote(f.Value.Str)
	case ValInt, ValFloat, ValBool:
		s += fmt.Sprintf("=%s:%g", f.Value.Kind, f.Value.Num)
	case ValDuration:
		s += fmt.Sprintf("=%gs", f.Value.Num)
	case ValNone:
		if f.Kind != FactRoute {
			s += "=?"
		}
	}
	s += "@" + strconv.Itoa(f.Line)
	if f.Value.Path != "" {
		s += fmt.Sprintf(" (from %s:%d)", f.Value.Path, f.Value.Line)
	}
	if len(f.Aliases) > 0 {
		s += " aka " + strings.Join(f.Aliases, ",")
	}
	if f.Kind == FactRoute && !f.Anchored {
		s += " unanchored"
	}
	return s
}

func extractOne(t *testing.T, srcs ...Source) []string {
	t.Helper()
	out := []string{}
	for _, f := range ExtractFacts(srcs) {
		out = append(out, render(f))
	}
	return out
}

func TestFactsConstants(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"literals and units", `package p
const (
	// Workers is the pool size.
	Workers = 8
	Ratio = 0.5
	Name = "docsp"
	On = true
	Timeout = 30 * time.Second
	Big = 1 << 20
	Neg = -3
	private = "x"
	Sum = Workers + 2
	Dur = time.Duration(5) * time.Minute
	Unknown = runtime.GOOS
)`, []string{
			"const Workers=int:8@4", "const Ratio=float:0.5@5", `const Name="docsp"@6`, "const On=bool:1@7",
			"const Timeout=30s@8", "const Big=int:1.048576e+06@9", "const Neg=int:-3@10", `const private="x"@11`,
			"const Sum=int:10@12", "const Dur=300s@13", "const Unknown=?@14",
		}},
		{"iota continuation has no value of its own", `package p
const (
	A = iota
	B
)`, []string{"const A=?@3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractOne(t, Source{Path: "p/p.go", Src: []byte(c.src)})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestFactsConstantsAcrossFiles(t *testing.T) {
	got := extractOne(t,
		Source{Path: "cmd/app/main.go", Src: []byte(`package main
func main() {
	flag.Int("workers", defaultWorkers, "pool size")
	flag.Int("questions", limits.MaxQuestions, "questions")
	flag.Int("bad", other.Missing, "unknown package constant")
}`)},
		Source{Path: "cmd/app/defaults.go", Src: []byte("package main\nconst defaultWorkers = 4\n")},
		Source{Path: "internal/limits/limits.go", Src: []byte("package limits\nconst MaxQuestions = 150\n")},
		Source{Path: "internal/limits/limits_test.go", Src: []byte("package limits\nconst MaxQuestions = 1\n")},
	)
	want := []string{
		"const defaultWorkers=int:4@2",
		"flag workers=int:4@3 (from cmd/app/defaults.go:2)",
		"flag questions=int:150@4 (from internal/limits/limits.go:2)",
		"flag bad=?@5",
		"const MaxQuestions=int:150@2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestFactsFlags(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"stdlib", `package main
func main() {
	flag.Int("workers", 8, "number of workers")
	fs.StringVar(&s, "format", "json", "output format")
	flag.Duration("timeout", 90*time.Second, "request timeout")
	flag.Float64("ratio", 1, "ratio")
	flag.Bool("dry-run", false, "no changes")
	flag.Int("computed", runtime.NumCPU(), "cpus")
	flag.Int("short", 1) // not a flag definition: wrong arity
}`, []string{
			"flag workers=int:8@3", `flag format="json"@4`, "flag timeout=90s@5", "flag ratio=float:1@6",
			"flag dry-run=bool:0@7", "flag computed=?@8",
		}},
		{"pflag and cobra shorthands", `package cli
func init() {
	root.Flags().IntP("port", "p", 8080, "listen port")
	root.PersistentFlags().StringVarP(&cfg, "config", "c", "", "config file")
}`, []string{"flag port=int:8080@3 aka p", `flag config=""@4 aka c`}},
		{"multi-line call quotes the default's line", `package main
func main() {
	flag.Int(
		"workers",
		8,
		"pool size",
	)
}`, []string{"flag workers=int:8@4 (from p/p.go:5)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractOne(t, Source{Path: "p/p.go", Src: []byte(c.src)})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestFactsEnv(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"getenv without default", `package p
func f() { _ = os.Getenv("APP_HOME") }`, []string{"env APP_HOME=?@2"}},
		{"if empty then default", `package p
func f() string {
	v := os.Getenv("APP_LEVEL")
	if v == "" {
		v = "info"
	}
	return v
}`, []string{`env APP_LEVEL="info"@3`}},
		{"lookupenv not ok then default", `package p
func f() {
	port, ok := os.LookupEnv("APP_PORT")
	if !ok {
		port = "8080"
	}
}`, []string{`env APP_PORT="8080"@3`}},
		{"cmp.Or default", `package p
func f() { _ = cmp.Or(os.Getenv("APP_MODE"), "fast") }`, []string{`env APP_MODE="fast"@2`}},
		{
			"helper with default", `package p
func f() { _ = envOr("APP_WORKERS", 4); _ = mustEnv("APP_TOKEN"); t.Setenv("APP_X", "1") }`,
			[]string{"env APP_WORKERS=int:4@2", "env APP_TOKEN=?@2"},
		},
		{"default assigned elsewhere is not a default", `package p
func f() {
	v := os.Getenv("APP_A")
	if v == "" {
		log.Print("unset")
	}
}`, []string{"env APP_A=?@3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractOne(t, Source{Path: "p/p.go", Src: []byte(c.src)})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestFactsRoutes(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"net/http 1.22 patterns", `package p
func routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /items/{id}", getItem)
	mux.Handle("/healthz", health)
	http.HandleFunc("POST /items", s.createItem)
	mux.HandleFunc("example.com/", h)
}`, []string{"route GET /items/{}@3", "route /healthz@4", "route POST /items@5"}},
		{"chi gin echo", `package p
func routes() {
	r.Get("/users/{userID}", listUsers)
	r.Method("DELETE", "/users/{id}", del)
	g.POST("/orders/:id", h.Create)
	e.PUT("/orders", func(c echo.Context) error { return nil })
	cache.Get("/key", nil)
	_, _ = http.Get("/x")
}`, []string{"route GET /users/{}@3", "route DELETE /users/{}@4", "route POST /orders/{}@5", "route PUT /orders@6"}},
		{"groups are not anchored", `package p
func routes() {
	api := r.Group("/api")
	api.GET("/items", list)
	r.Route("/v1", func(r chi.Router) { r.Get("/things", list) })
	r.Get("/root", root)
}`, []string{"route GET /items@4 unanchored", "route GET /things@5 unanchored", "route GET /root@6"}},
		{"a mounted prefix unanchors every route", `package p
func routes() {
	mux.HandleFunc("GET /items", list)
	http.Handle("/api/", http.StripPrefix("/api", mux))
}`, []string{"route GET /items@3 unanchored", "route /api/@4 unanchored"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractOne(t, Source{Path: "p/p.go", Src: []byte(c.src)})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestFactsConfigFields(t *testing.T) {
	src := `package config
type Config struct {
	// Workers is the pool size.
	Workers int           ` + "`yaml:\"workers\" json:\"workers,omitempty\"`" + `
	Timeout time.Duration ` + "`yaml:\"timeout\"`" + `
	Name    string
}
func NewConfig() *Config {
	return &Config{Workers: 8, Timeout: 30 * time.Second, Name: hostname()}
}
func build() Config { return Config{Workers: 1} }
`
	got := extractOne(t, Source{Path: "config/config.go", Src: []byte(src)})
	want := []string{
		"field Workers=int:8@9 aka Config.Workers,workers",
		"field Timeout=30s@9 aka Config.Timeout,timeout",
		"field Name=?@9 aka Config.Name",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestFactsSkipsTestsAndBrokenFiles(t *testing.T) {
	got := extractOne(t,
		Source{Path: "a_test.go", Src: []byte("package a\nconst X = 1\n")},
		Source{Path: "testdata/b.go", Src: []byte("package b\nconst Y = 1\n")},
		Source{Path: "c.go", Src: []byte("package c\nconst (")},
	)
	if len(got) != 0 {
		t.Errorf("got %q, want nothing", got)
	}
}

func TestNormalizeRoute(t *testing.T) {
	for in, want := range map[string]string{
		"/items/{id}": "/items/{}", "/items/:id": "/items/{}", "/files/*rest": "/files/{}", "/a/{p...}": "/a/{}", "/plain": "/plain",
	} {
		if got := NormalizeRoute(in); got != want {
			t.Errorf("NormalizeRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzExtractFacts(f *testing.F) {
	f.Add("package p\nconst A = 1 << 2\nfunc f() { flag.Int(\"x\", A, \"u\") }\n")
	f.Add("package p\nconst A = B\nconst B = A\n")
	f.Fuzz(func(_ *testing.T, src string) {
		_ = ExtractFacts([]Source{{Path: "p/p.go", Src: []byte(src)}})
	})
}
