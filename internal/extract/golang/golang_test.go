package golang

import (
	"reflect"
	"testing"

	"github.com/MIERAI/docs-platform/internal/extract/extracttest"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name, path, src string
		want            []string
	}{
		{"stdlib flags and env", "cmd/app/main.go", `package main
import ("flag"; "os")
const envName = "APP_MODE"
func main() {
	flag.String("listen", ":80", "addr")
	fs := flag.NewFlagSet("x", flag.ExitOnError)
	fs.BoolVar(&b, "dry-run", false, "no changes")
	_ = os.Getenv(envName)
	_, _ = os.LookupEnv("APP_HOME")
	_ = envOr("APP_LEVEL", "info")
	_ = flag.Arg(0)
}`, []string{
			"cli_flag --dry-run [app] public", "cli_flag --listen [app] public",
			"env_var APP_HOME [app] public", "env_var APP_LEVEL [app] public", "env_var APP_MODE [app] public",
			"program app public",
		}},
		{
			"cobra and pflag", "internal/cli/root.go", `package cli
import "github.com/spf13/cobra"
var root = &cobra.Command{Use: "serve [flags]", Aliases: []string{"s"}}
func init() { root.Flags().StringP("config", "c", "", "config file"); root.PersistentFlags().Int("port", 1, "p") }`,
			[]string{"cli_command s", "cli_command serve public", "cli_flag --config public", "cli_flag --port public", "cli_flag -c"},
		},
		{
			"metrics", "metrics.go", `package m
import "github.com/prometheus/client_golang/prometheus"
var a = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "app", Subsystem: "http", Name: "requests_total"})
var d = prometheus.NewDesc("app_up", "up", nil, nil)
func f() { meter.Int64Counter("app.jobs.done"); fmt.Fprintln(w, "# TYPE app_queue_depth gauge") }`,
			[]string{"metric app.jobs.done public", "metric app_http_requests_total public", "metric app_jobs_done", "metric app_queue_depth public", "metric app_up public"},
		},
		{
			"sub-command switch", "cmd/tool/main.go", `package main
import "os"
func main() { switch os.Args[1] { case "check", "help": ; case "-x": } }`,
			[]string{"cli_command check [tool] public", "cli_command help [tool]", "program tool public"},
		},
		{
			"test files only contribute env", "pkg/x_test.go", `package pkg
import ("os"; "flag")
var f = flag.String("t", "", "x")
func TestX() { _ = os.Getenv("GATE_MAX") }`,
			[]string{"env_var GATE_MAX public"},
		},
		{"setters are not reads", "pkg/y.go", `package pkg
func f() { setEnv("NOT_READ", "1"); t.Setenv("ALSO_NOT", "") }`, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extracttest.Symbols(t, Extractor{}, c.path, c.src)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("symbols:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestSyntaxErrorIsAnError(t *testing.T) {
	if _, err := (Extractor{}).Extract(plugin.File{Path: "x.go", Content: []byte("package")}); err == nil {
		t.Error("want a parse error (reported as a coverage gap)")
	}
}

func FuzzExtract(f *testing.F) {
	f.Add([]byte("package main\nfunc main(){ flag.String(\"a\",\"\",\"\") }"))
	f.Add([]byte("package p\nvar _ = prometheus.CounterOpts{Name: x}"))
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = Extractor{}.Extract(plugin.File{Path: "cmd/x/main.go", Content: b})
	})
}
