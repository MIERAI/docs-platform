// Command docsp keeps documentation true to the code.
//
//	docsp check [flags] [repo]          layer ③ reference check → findings/v1 JSON + summary
//	docsp impact --base REF [--head REF] [flags] [repo]
//	                                    layer ④(a) PR impact check → impact/v1 JSON
//	docsp audit [flags] [repo]          layer ④(c) semantic audit → findings/v1 JSON (proposals)
//	docsp eval-model -set FILE [flags]  score a model provider per task on a frozen eval set
//	docsp import-findings -repo-name N FILE.jsonl
//	                                    reviewed backlog → findings/v1 JSON
//	docsp extract [repo]                dump what the extractors found (extract/v1)
//	docsp validate FILE                 validate a findings/v1 document
//	docsp judge-tools-mcp -spec FILE    (internal) serve the read-only judge-tools over MCP stdio
//	docsp version
package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "0.4.1"

// exit codes.
const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	var err error
	code := exitOK
	switch args[0] {
	case "check":
		code, err = cmdCheck(ctx, args[1:], stdout, stderr)
	case "impact":
		code, err = cmdImpact(ctx, args[1:], stdout, stderr)
	case "audit":
		code, err = cmdAudit(ctx, args[1:], stdout, stderr)
	case "eval-model":
		code, err = cmdEvalModel(ctx, args[1:], stdout, stderr)
	case "import-findings":
		code, err = cmdImportFindings(args[1:], stdout, stderr)
	case "extract":
		code, err = cmdExtract(args[1:], stdout, stderr)
	case "validate":
		code, err = cmdValidate(args[1:], stdout)
	case "judge-tools-mcp":
		code, err = cmdJudgeToolsMCP(ctx, args[1:], os.Stdin, stdout, stderr)
	case "version", "--version":
		_, err = fmt.Fprintln(stdout, "docsp", version)
	case "help", "-h", "--help":
		usage(stdout)
	default:
		usage(stderr)
		return exitUsage
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "docsp:", err)
		if code == exitOK {
			code = exitUsage
		}
	}
	return code
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: docsp <command> [flags] [repo]

commands:
  check      that documentation references exist in code and public symbols are documented
  audit      semantic audit: claims in the docs vs the code (deterministic checks + optional judge model)
  impact     doc sections a change affects (derived from the reference graph) + explicit rules / waivers
  eval-model score a model provider per task (judge, draft) on a frozen eval set; -dry-run estimates cost
  import-findings
             turn a reviewed backlog (JSON Lines) into findings/v1 for the ledger
  extract    print what the extractors found (docs-platform/extract/v1)
  validate   a findings/v1 document
  version    print the version

run "docsp <command> -h" for the command's flags
`)
}
