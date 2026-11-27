package ledger

import (
	"strings"
	"testing"
)

const goSrc = `package jobs

import "fmt"

const (
	// SLA in seconds.
	slaSeconds = 300.0

	healthOK = "OK"
)

var rules = map[string]int{
	"sla":    300,
	"health": 1,
}

// Summary sums.
func Summary(d float64) string {
	if d > slaSeconds {
		return "DEGRADED"
	}
	return fmt.Sprint(healthOK)
}

func (s *Server) Run() {}

type Job struct {
	ID int
}
`

func TestComputeScopeGo(t *testing.T) {
	for _, tt := range []struct {
		line       int
		name       string
		start, end int
	}{
		{7, "const slaSeconds", 6, 7},
		{9, "const healthOK", 9, 9},
		{13, `var rules["sla"]`, 13, 13},
		{14, `var rules["health"]`, 14, 14},
		{12, "var rules", 12, 15},
		{19, "func Summary", 17, 23},
		{17, "func Summary", 17, 23},
		{25, "func (*Server).Run", 25, 25},
		{28, "type Job", 27, 29},
	} {
		s := ComputeScope("jobs.go", goSrc, tt.line)
		if s == nil || s.Kind != ScopeGoDecl || s.Name != tt.name || s.Start != tt.start || s.End != tt.end {
			t.Errorf("line %d: %+v, want %s %d-%d", tt.line, s, tt.name, tt.start, tt.end)
		}
	}
	// outside every declaration, or a file that does not parse: the line ±3
	if s := ComputeScope("jobs.go", goSrc, 1); s.Kind != ScopeLines || s.Start != 1 || s.End != 4 {
		t.Errorf("package clause: %+v", s)
	}
	if s := ComputeScope("bad.go", "package x\nfunc (\n", 2); s.Kind != ScopeLines {
		t.Errorf("unparsable: %+v", s)
	}
	if s := ComputeScope("jobs.go", goSrc, 99); s != nil {
		t.Errorf("out of range: %+v", s)
	}
}

const yamlSrc = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 3
  template:
    spec:
      containers:
        - name: app
          image: app:1.2

          # the sidecar
        - name: proxy
          image: envoy:1
---
kind: Service
metadata:
  name: web
spec:
  port: 80
`

func TestComputeScopeYAML(t *testing.T) {
	for _, tt := range []struct {
		line       int
		name       string
		start, end int
	}{
		{6, "Deployment:web/spec.replicas", 6, 6},
		{11, "Deployment:web/spec.template.spec.containers[name=app].image", 11, 11},
		{10, "Deployment:web/spec.template.spec.containers[name=app]", 10, 11},
		{15, "Deployment:web/spec.template.spec.containers[name=proxy].image", 15, 15},
		{5, "Deployment:web/spec", 5, 15},
		{21, "Service:web/spec.port", 21, 21},
	} {
		s := ComputeScope("deploy.yaml", yamlSrc, tt.line)
		if s == nil || s.Kind != ScopeYAMLKey || s.Name != tt.name || s.Start != tt.start || s.End != tt.end {
			t.Errorf("line %d: %+v, want %s %d-%d", tt.line, s, tt.name, tt.start, tt.end)
		}
	}
	if s := ComputeScope("c.yml", "port: 9090\n", 1); s.Name != "port" || s.Start != 1 || s.End != 1 {
		t.Errorf("single document: %+v", s)
	}
	if s := ComputeScope("bad.yaml", "a: [\n", 1); s.Kind != ScopeLines {
		t.Errorf("unparsable: %+v", s)
	}
}

func TestComputeScopeLines(t *testing.T) {
	src := strings.Repeat("x\n", 10)
	if s := ComputeScope("main.tf", src, 5); s.Kind != ScopeLines || s.Start != 2 || s.End != 8 {
		t.Errorf("%+v", s)
	}
	if s := ComputeScope("main.tf", src, 10); s.Start != 7 || s.End != 10 {
		t.Errorf("end of file: %+v", s)
	}
}
