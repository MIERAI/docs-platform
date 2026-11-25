# Developer entry points. CI runs the same targets.
GO        ?= go
FUZZTIME  ?= 30s
LINT      ?= golangci-lint
VULNCHECK ?= govulncheck

# Every parser of external input has a fuzz target: "package FuzzTarget" per line.
FUZZ_LIST = scripts/fuzz-targets.txt

# The Anthropic Messages API provider is its own module (keeps the core at one dependency).
PROVIDERS = providers/anthropic

.PHONY: all build build-replay providers test race lint vuln fuzz golden check-self ci

all: lint test

build:
	$(GO) build -trimpath -o bin/docsp ./cmd/docsp

# offline judge/v1 provider answering from canned answers (tests, re-scoring frozen runs)
build-replay:
	$(GO) build -trimpath -o bin/docsp-judge-replay ./cmd/docsp-judge-replay

providers:
	cd providers/anthropic && $(GO) build -trimpath -o ../../bin/docsp-judge-anthropic .

test:
	$(GO) test ./...
	for p in $(PROVIDERS); do (cd $$p && $(GO) test ./...) || exit 1; done

race:
	$(GO) test -race ./...
	for p in $(PROVIDERS); do (cd $$p && $(GO) test -race ./...) || exit 1; done

lint:
	$(LINT) run ./...
	for p in $(PROVIDERS); do (cd $$p && $(LINT) run --config $(CURDIR)/.golangci.yml ./...) || exit 1; done

vuln:
	$(VULNCHECK) ./...
	for p in $(PROVIDERS); do (cd $$p && $(VULNCHECK) ./...) || exit 1; done

fuzz:
	GO=$(GO) FUZZTIME=$(FUZZTIME) ./scripts/fuzz.sh $(FUZZ_LIST)

golden:
	$(GO) test ./internal/check -update

# dogfooding: docs-platform checks its own documentation
check-self: build
	./bin/docsp check -format text -fail-on medium .

ci: lint race vuln check-self
