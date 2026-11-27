#!/usr/bin/env bash
# The offline tier of REPRODUCE.md: no model, no API key, network only for Go modules and
# for cloning the three public benchmark repositories from GitHub at their pinned revisions.
# Every headline number is compared with bench/data/expected-offline.json; any mismatch fails.
#
#   scripts/repro-offline.sh            (or: make repro-offline)
#
# Environment: GO (default go), REPRO_WORK (scratch directory, default $TMPDIR/docsp-repro;
# reused between runs, never inside this repository), SKIP_TESTS=1 (skip the unit tests).
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT=$(pwd)
GO=${GO:-go}
WORK=${REPRO_WORK:-${TMPDIR:-/tmp}/docsp-repro}
WORK=${WORK%/}
DATA=bench/data

case "$WORK/" in
"$ROOT"/*) echo "REPRO_WORK must be outside the repository ($WORK)" >&2; exit 2 ;;
esac
mkdir -p "$WORK"
WORK=$(cd "$WORK" && pwd)

step() { printf '\n== %s\n' "$*"; }

if [ "${SKIP_TESTS:-0}" != 1 ]; then
	step "1/8 unit tests (core and providers/anthropic)"
	"$GO" test ./...
	(cd providers/anthropic && "$GO" test ./...)
fi

step "2/8 build bin/docsp and bin/docsp-judge-replay"
"$GO" build -trimpath -o bin/docsp ./cmd/docsp
"$GO" build -trimpath -o bin/docsp-judge-replay ./cmd/docsp-judge-replay

step "3/8 clone the public repositories at their pinned revisions into $WORK"
"$GO" run ./bench fetch -manifest "$DATA/manifest.json" -work "$WORK"

# Instance configurations need absolute program paths (the exec provider refuses others).
instance() { # $1 = canned answers file, $2 = classification line
	cat <<EOF
version: 1
ai:
  judge:
    provider: exec
    model: canned
    timeout_seconds: 120
    max_concurrency: 4
    exec:
      command: ["$ROOT/bin/docsp-judge-replay", "-answers", "$ROOT/$1", "-probe"]
      local: true
      modes: [agentic]
    # the frozen answers were given to the evaluated docsp-agentic/3 prompt (no rubric)
    agentic: {rubric: false}
classification:
  default: private
  repos: {$2}
EOF
}
instance "$DATA/answers/test-frozen.jsonl" "gitops-manifests: public, infrastructure-live: public" >"$WORK/replay-test.yaml"
instance "$DATA/answers/dev-frozen.jsonl" "api-factory: public" >"$WORK/replay-dev.yaml"

step "4/8 agentic TEST replay: frozen prototype answers through the current harness (writes the claim ledger)"
rm -rf "$WORK/out-test" "$WORK/ledger.json" && mkdir -p "$WORK/out-test"
bin/docsp eval-model -set "$DATA/eval/agentic-test.jsonl" -instance-config "$WORK/replay-test.yaml" \
	-mode agentic -no-drift-cache -out-dir "$WORK/out-test" -ledger "$WORK/ledger.json" \
	-worktree "gitops-manifests=$WORK/gitops-manifests" -worktree "infrastructure-live=$WORK/infrastructure-live"

step "5/8 agentic DEV replay"
rm -rf "$WORK/out-dev" && mkdir -p "$WORK/out-dev"
bin/docsp eval-model -set "$DATA/eval/agentic-dev.jsonl" -instance-config "$WORK/replay-dev.yaml" \
	-mode agentic -no-drift-cache -out-dir "$WORK/out-dev" -worktree "api-factory=$WORK/api-factory"

step "6/8 ledger replay over real history (evidence-based invalidation)"
"$GO" run ./bench ledger-replay -ledger "$WORK/ledger.json" -gt "$DATA/groundtruth" \
	-set "$DATA/eval/agentic-test.jsonl" -json "$WORK/ledger-replay.json" \
	-repo "gitops-manifests=$WORK/gitops-manifests" -repo "infrastructure-live=$WORK/infrastructure-live"

step "7/8 history replay (change-time impact) and the frozen snippet eval set"
"$GO" run ./bench replay -gt "$DATA/groundtruth" -work "$WORK" -manifest "$DATA/manifest.json" \
	-json "$WORK/replay.json" -md "$WORK/replay.md" -eval-out "$WORK/snippet-evalset.jsonl"

step "8/8 regenerate the mutation set, then compare every number with $DATA/expected-offline.json"
"$GO" run ./bench mutate -manifest "$DATA/manifest.json" -work "$WORK" -out "$WORK/mutations.json"
"$GO" run ./bench verify-offline -expected "$DATA/expected-offline.json" \
	-eval-test "$(ls "$WORK"/out-test/eval-judge-*.json)" -eval-dev "$(ls "$WORK"/out-dev/eval-judge-*.json)" \
	-ledger-replay "$WORK/ledger-replay.json" -replay "$WORK/replay.json" \
	-evalset "$WORK/snippet-evalset.jsonl" -mutations "$WORK/mutations.json"
