// Command docsp keeps documentation true to the code.
//
//	docsp check [flags] [repo]          layer ③ reference check → findings/v1 JSON + summary
//	docsp impact --base REF [--head REF] [flags] [repo]
//	                                    layer ④(a) PR impact check → impact/v1 JSON
//	docsp audit [flags] [repo]          layer ④(c) semantic audit → findings/v1 JSON (proposals)
//	docsp extract [repo]                dump what the extractors found (extract/v1)
//	docsp validate FILE                 validate a findings/v1 document
//	docsp version
package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "0.1.0"

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
	case "extract":
		code, err = cmdExtract(args[1:], stdout, stderr)
	case "validate":
		code, err = cmdValidate(args[1:], stdout)
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
  audit      semantic audit: claims in the docs vs the code (deterministic checks + optional local judge)
  impact     check that a change touching mapped code also touches its docs (or carries a waiver)
  extract    print what the extractors found (docs-platform/extract/v1)
  validate   a findings/v1 document
  version    print the version

run "docsp <command> -h" for the command's flags
`)
}
