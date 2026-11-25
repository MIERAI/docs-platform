package golang

import (
	"go/ast"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
)

// flagMethods maps flag-definition method names to the index of the flag-name argument and
// the minimum argument count of a real flag definition.
var flagMethods = func() map[string][2]int {
	m := map[string][2]int{}
	types := []string{
		"String", "Bool", "Int", "Int64", "Int32", "Uint", "Uint64", "Float64", "Float32", "Duration",
		"StringSlice", "StringArray", "IntSlice", "StringToString", "Count", "IP", "Func", "BoolFunc", "Text",
	}
	for _, t := range types {
		m[t] = [2]int{0, 3}
		m[t+"P"] = [2]int{0, 4}
		m[t+"Var"] = [2]int{1, 4}
		m[t+"VarP"] = [2]int{1, 5}
	}
	m["Var"] = [2]int{1, 3}
	m["VarP"] = [2]int{1, 4}
	m["TextVar"] = [2]int{1, 4}
	m["Count"] = [2]int{0, 2}
	m["CountP"] = [2]int{0, 3}
	m["Func"] = [2]int{0, 3}
	return m
}()

var (
	reFlagName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	reEnvName  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	reEnvFunc  = regexp.MustCompile(`(?i)env`)
	reEnvWrite = regexp.MustCompile(`(?i)^(set|unset|clear)`)
)

// otelInstruments are OpenTelemetry meter methods whose first argument is the metric name.
var otelInstruments = map[string]bool{}

func init() {
	for _, n := range []string{"Int64", "Float64"} {
		for _, k := range []string{
			"Counter", "UpDownCounter", "Histogram", "Gauge",
			"ObservableCounter", "ObservableUpDownCounter", "ObservableGauge",
		} {
			otelInstruments[n+k] = true
		}
	}
}

func (v *visitor) call(c *ast.CallExpr) {
	name, recv := callName(c.Fun)
	switch {
	case (recv == "os" || recv == "syscall") && (name == "Getenv" || name == "LookupEnv"):
		v.env(c, model.ConfHigh)
	case reEnvFunc.MatchString(name) && recv != "os" && !reEnvWrite.MatchString(name) && !v.test:
		v.env(c, model.ConfMedium)
	case recv == "prometheus" && name == "NewDesc":
		v.metricArg(c, 0)
	case otelInstruments[name]:
		v.metricArg(c, 0)
	}
	if spec, ok := flagMethods[name]; ok {
		v.flag(c, name, recv, spec)
	}
}

// callName returns the called function name and a receiver hint: the package/ident name, or
// "Flags" for (...).Flags()/PersistentFlags() receivers.
func callName(fun ast.Expr) (string, string) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, ""
	case *ast.SelectorExpr:
		switch x := f.X.(type) {
		case *ast.Ident:
			return f.Sel.Name, x.Name
		case *ast.CallExpr:
			if inner, _ := callName(x.Fun); strings.HasSuffix(inner, "Flags") {
				return f.Sel.Name, "Flags"
			}
		}
		return f.Sel.Name, ""
	}
	return "", ""
}

func (v *visitor) env(c *ast.CallExpr, conf model.Confidence) {
	if len(c.Args) == 0 {
		return
	}
	if s, ok := v.str(c.Args[0]); ok && reEnvName.MatchString(s) {
		v.add(model.KindEnvVar, s, c.Pos(), true, conf)
	}
}

func (v *visitor) flag(c *ast.CallExpr, method, recv string, spec [2]int) {
	idx, minArgs := spec[0], spec[1]
	if len(c.Args) < minArgs || idx >= len(c.Args) {
		return
	}
	name, ok := v.str(c.Args[idx])
	if !ok || !reFlagName.MatchString(name) {
		return
	}
	conf := model.ConfMedium
	if recv == "flag" || recv == "pflag" || recv == "Flags" {
		conf = model.ConfHigh
	}
	long := "-" + name
	if recv != "flag" || len(name) > 1 {
		long = "--" + name
	}
	v.add(model.KindCLIFlag, long, c.Pos(), true, conf)
	if strings.HasSuffix(method, "P") && idx+1 < len(c.Args) {
		if short, ok := v.str(c.Args[idx+1]); ok && len(short) == 1 {
			v.add(model.KindCLIFlag, "-"+short, c.Pos(), false, conf)
		}
	}
}

func (v *visitor) metricArg(c *ast.CallExpr, i int) {
	if i >= len(c.Args) {
		return
	}
	if s, ok := v.str(c.Args[i]); ok && s != "" {
		v.add(model.KindMetric, s, c.Pos(), true, model.ConfHigh)
		if u := strings.NewReplacer(".", "_", "-", "_").Replace(s); u != s {
			v.add(model.KindMetric, u, c.Pos(), false, model.ConfMedium)
		}
	}
}
