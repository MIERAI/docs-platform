package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
)

// TestContractCommand: the design and the code live in two repositories of a group; the
// design is read at a declared revision with git show (its checkout is elsewhere).
func TestContractCommand(t *testing.T) {
	root := t.TempDir()
	spec, code := filepath.Join(root, "spec"), filepath.Join(root, "code")
	for dir, files := range map[string]map[string]string{
		spec: {"api/shop.yaml": "openapi: 3.0.0\npaths:\n  /graphql:\n    post:\n      requestBody:\n        content:\n          application/json:\n            examples:\n              a: { value: { query: \"query ($code: String!) { shelf(code: $code) { code title } }\" } }\n"},
		code: {"shop/src/main/kotlin/Shop.kt": "@GraphQLApi\nclass Shop {\n    @Query\n    fun getShelf(code: String): ShelfDto = TODO()\n}\n\ndata class ShelfDto(val code: String, val name: String)\n"},
	} {
		for rel, content := range files {
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "init"}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s", args, out)
			}
		}
	}
	inst := filepath.Join(root, "instance.yaml")
	cfg := `version: 1
groups:
  - name: shop
    repos: [{name: spec, role: api-spec, path: ` + spec + `}, {name: code, role: code}]
    doc_sets:
      - name: graphql
        repo: spec
        docs: ["api/**"]
        targets: [{repo: code, ref: main}]
        contract: {design_adapter: openapi-graphql, code_adapter: kotlin-graphql, design_ref: main, authority: design}
`
	if err := os.WriteFile(inst, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code2 := run(context.Background(), []string{"contract", "-instance-config", inst, "-checkout", "code=" + code, "-format", "json"}, &out, &errb)
	if code2 != exitOK {
		t.Fatalf("exit %d: %s", code2, errb.String())
	}
	sch, err := jsonschema.Compile(contracts.Contract)
	if err != nil {
		t.Fatal(err)
	}
	if errs := sch.ValidateJSON(out.Bytes()); len(errs) > 0 {
		t.Fatalf("contract/v1: %v", errs)
	}
	var doc struct {
		DocSet string `json:"doc_set"`
		Items  []struct {
			Kind, Subject, Message string
		} `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range doc.Items {
		got = append(got, it.Kind+" "+it.Subject)
		if !strings.HasPrefix(it.Message, "code deviates from design: ") {
			t.Errorf("message %q", it.Message)
		}
	}
	if doc.DocSet != "graphql" || strings.Join(got, ";") != "output.missing_in_code outputs.title;output.missing_in_design outputs.name" {
		t.Errorf("doc set %q items %v", doc.DocSet, got)
	}
	var text bytes.Buffer
	if c := run(context.Background(), []string{"contract", "-instance-config", inst, "-checkout", "code=" + code, "-no-history"}, &text, &errb); c != exitOK || !strings.Contains(text.String(), "overlap 1") {
		t.Errorf("text %d: %s", c, text.String())
	}
	if c := run(context.Background(), []string{"contract", "-design", spec}, &text, &errb); c != exitUsage {
		t.Errorf("missing flags: exit %d", c)
	}
}
