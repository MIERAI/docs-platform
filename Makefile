# Developer entry points. CI runs the same targets.
GO        ?= go
FUZZTIME  ?= 30s
# Tools are pinned and built from source with `go run` (no binary download); the version
# matches .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION   ?= v1.9.0
GOLANGCI_LINT ?= $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   ?= $(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# Every parser of external input has a fuzz target: "package FuzzTarget" per line.
FUZZ_LIST = scripts/fuzz-targets.txt

# The Anthropic Messages API provider is its own module (keeps the core at one dependency).
PROVIDERS = providers/anthropic

.PHONY: all build build-replay providers test race lint vuln fuzz golden check-self repro-offline ci

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
	$(GOLANGCI_LINT) run ./...
	for p in $(PROVIDERS); do (cd $$p && $(GOLANGCI_LINT) run --config $(CURDIR)/.golangci.yml ./...) || exit 1; done

vuln:
	$(GOVULNCHECK) ./...
	for p in $(PROVIDERS); do (cd $$p && $(GOVULNCHECK) ./...) || exit 1; done

fuzz:
	GO=$(GO) FUZZTIME=$(FUZZTIME) ./scripts/fuzz.sh $(FUZZ_LIST)

golden:
	$(GO) test ./internal/check -update

# dogfooding: docs-platform checks its own documentation
check-self: build
	./bin/docsp check -format text -fail-on medium .

# REPRODUCE.md, offline tier: unit tests, replay of the frozen answers, ledger replay,
# history replay, mutation set; fails when a number differs from bench/data/expected-offline.json.
# REPRO_WORK=DIR chooses the scratch directory (default $TMPDIR/docsp-repro).
repro-offline:
	GO=$(GO) ./scripts/repro-offline.sh

ci: lint race vuln check-self
