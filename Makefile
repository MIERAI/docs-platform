# Developer entry points. CI runs the same targets.
GO        ?= go
FUZZTIME  ?= 30s
LINT      ?= golangci-lint
VULNCHECK ?= govulncheck

# Every parser of external input has a fuzz target: "package FuzzTarget" per line.
FUZZ_LIST = scripts/fuzz-targets.txt

.PHONY: all build test race lint vuln fuzz golden check-self ci

all: lint test

build:
	$(GO) build -trimpath -o bin/docsp ./cmd/docsp

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

lint:
	$(LINT) run ./...

vuln:
	$(VULNCHECK) ./...

fuzz:
	GO=$(GO) FUZZTIME=$(FUZZTIME) ./scripts/fuzz.sh $(FUZZ_LIST)

golden:
	$(GO) test ./internal/check -update

# dogfooding: docs-platform checks its own documentation
check-self: build
	./bin/docsp check -format text -fail-on medium .

ci: lint race vuln check-self
