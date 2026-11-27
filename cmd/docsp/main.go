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
//	docsp contract -design DIR -design-adapter A -code DIR -code-adapter B [flags]
//	                                    design-first spec vs code (contract/v1; no model)
//	docsp stale -ledger FILE [-ref REV] [repo]
//	                                    claims whose evidence changed (no model)
//	docsp draft verify -log FILE [-ledger FILE] [-claim ID] [repo]
//	                                    machine-verify drafts (draft-verify/v1; no model)
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
var version = "0.4.2"

// exit codes.
const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// command runs one sub-command.
type command func(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error)

// commands maps sub-command names to their implementations.
var commands = map[string]command{
	"check":            cmdCheck,
	"impact":           cmdImpact,
	"audit":            cmdAudit,
	"eval-model":       cmdEvalModel,
	"stale":            cmdStale,
	"schedule":         cmdSchedule,
	"recheck":          cmdRecheck,
	"sample":           cmdSample,
	"slo":              cmdSLO,
	"group":            cmdGroup,
	"contract":         cmdContract,
	"factory-rules":    cmdFactoryRules,
	"factory-pr":       cmdFactoryPR,
	"pr-comment":       cmdPRComment,
	"factory-coverage": cmdFactoryCoverage,
	"report":           cmdReport,
	"draft":            cmdDraft,
	"drafts":           cmdDrafts,
	"import-findings": func(_ context.Context, a []string, out, errw io.Writer) (int, error) {
		return cmdImportFindings(a, out, errw)
	},
	"extract":  func(_ context.Context, a []string, out, errw io.Writer) (int, error) { return cmdExtract(a, out, errw) },
	"validate": func(_ context.Context, a []string, out, _ io.Writer) (int, error) { return cmdValidate(a, out) },
	"judge-tools-mcp": func(ctx context.Context, a []string, out, errw io.Writer) (int, error) {
		return cmdJudgeToolsMCP(ctx, a, os.Stdin, out, errw)
	},
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	var err error
	code := exitOK
	switch c, ok := commands[args[0]]; {
	case ok:
		code, err = c(ctx, args[1:], stdout, stderr)
	case args[0] == "version" || args[0] == "--version":
		_, err = fmt.Fprintln(stdout, "docsp", version)
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
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
  schedule   cycle count: the ledger's claims to judge again in this run (stale, due, quota), within budget
  recheck    judge a schedule's claims again (agentic) and update the ledger
  sample     draw the weekly accuracy sample of the ledger (an eval set for a paid re-audit)
  slo        documentation consistency SLIs (accuracy, freshness, phantom rate), error budget, cost
  report     validated problems for people: contradicted/dangling at once, the rest in batches; never fails
  draft      minimal-diff fix proposals for the ledger's problems (checked, logged, never applied)
  draft verify
             machine-verify drafts: format, re-extracted claims vs evidence, examples in a sandbox, blob guard
  draft apply
             write one draft into the checkout, refused when the document's blob moved
  drafts     what became of the drafts: acceptance, revert rate, diff size; docs to generate or link instead
  group      repository groups: plan (doc sets, declared targets, shared drift scans), stale, judge
  contract   design-first specs vs the code: interface diff + which side moved (git history; no model)
  factory-rules
             an API factory's business rules with hand-written code into the ledger, pinned to that code
  factory-pr a change from the design's side: touched rules judged at the head, factory-verified design drafts
  factory-coverage
             rule executable coverage (rule mutants vs the design's examples) + factory-verified example proposals
  stale      claims of the evidence ledger whose line or evidence changed since judged (no model)
  eval-model score a model provider per task (judge, draft) on a frozen eval set; -dry-run estimates cost
  import-findings
             turn a reviewed backlog (JSON Lines) into findings/v1 for the ledger
  extract    print what the extractors found (docs-platform/extract/v1)
  validate   a findings/v1 document
  version    print the version

run "docsp <command> -h" for the command's flags
`)
}
