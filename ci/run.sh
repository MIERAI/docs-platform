#!/usr/bin/env bash
# docs-platform CI (ci/action.yml): budget | main | pr | factory-pr | spend.
# Untrusted PR text never reaches this script; every input comes through the environment.
set -euo pipefail

D="$RUNNER_TEMP/docsp"
L="$RUNNER_TEMP/docsp-ledger"
INST="$RUNNER_TEMP/docsp-instance.yaml"
NAME="${GITHUB_REPOSITORY#*/}"
export DOCSP_SPEND_LOG="$RUNNER_TEMP/docsp-spend.log"
MONTH="$(date -u +%Y-%m)"

summary() { echo "$*" >>"$GITHUB_STEP_SUMMARY"; echo "$*"; }

budget() {
	local ai=true cap
	if [[ "${DOCSP_HAS_TOKEN:-false}" != true ]]; then
		ai=false; summary "docs-platform: no Claude token (a fork's PR gets none): deterministic only"
	fi
	if [[ "${DOCSP_PRIVATE:-false}" == true ]]; then
		ai=false; summary "docs-platform: a private repository's content never goes to a cloud model: no AI"
	fi
	local spent
	spent="$(gh api --paginate "repos/$GITHUB_REPOSITORY/actions/artifacts?per_page=100" \
		--jq ".artifacts[] | select(.name | startswith(\"docsp-usd-$MONTH-\")) | .name" |
		awk -F- '{s += $5} END {printf "%.4f", s + 0}')"
	case "$DOCSP_TASK" in main) cap=3 ;; *) cap=1 ;; esac
	[[ -n "${DOCSP_RUN_USD:-}" ]] && cap="$DOCSP_RUN_USD"
	local left
	left="$(awk -v m="$DOCSP_MONTHLY" -v s="$spent" 'BEGIN {printf "%.4f", m - s}')"
	cap="$(awk -v c="$cap" -v l="$left" 'BEGIN {printf "%.4f", (c < l ? c : l)}')"
	if awk -v l="$left" 'BEGIN {exit !(l < 0.05)}'; then
		ai=false; summary "docs-platform: the monthly AI budget (\$$DOCSP_MONTHLY) is spent (\$$spent in $MONTH): no AI until next month"
	fi
	summary "docs-platform: $MONTH spent \$$spent of \$$DOCSP_MONTHLY; this run may spend up to \$$cap (AI: $ai)"
	{ echo "ai=$ai"; echo "run_cap=$cap"; echo "left=$left"; } >>"$GITHUB_OUTPUT"
}

instance() { # $1 = the run's cap in USD
	cat >"$INST" <<EOF
version: 1
ai:
  judge:
    provider: claude-code
    model: $DOCSP_MODEL
    timeout_seconds: 600
    max_concurrency: 2
    budget: {max_requests: 200, max_usd: $1}
    claude_code: {program: $(command -v claude), pass_env: [CLAUDE_CODE_OAUTH_TOKEN]}
    agentic: {drift_max_steps: 30, drift_max_request_usd: 0.35, max_request_usd: 0.6}
classification:
  default: private
  repos:
    $NAME: public
consistency:
  max_age_days: 30
  churn_commits: 10
  classes:
    - {name: A, max_age_days: 14, docs: ["README.md"]}
  run_interval_days: 7
  budget: {max_claims: 60, usd_per_claim: 0.05}
  slo: {accuracy: 0.8, freshness: 0.95, phantom_rate: 0.01}
  sample_size: 50
  batch_days: 7
EOF
}

main_task() {
	mkdir -p "$L"
	if [[ "$DOCSP_AI" != true ]]; then
		summary "docs-platform: no AI this run: the ledger is kept as it is"
		return 0
	fi
	if [[ ! -s "$L/ledger.json" ]]; then
		local cap
		# the first run's own cap (bootstrap-usd), within what is left of the month
		cap="$(awk -v l="$DOCSP_LEFT" -v b="$DOCSP_BOOTSTRAP_USD" 'BEGIN {printf "%.4f", (b < l ? b : l) + 0}')"
		instance "$cap"
		summary "docs-platform: no ledger yet: building it (agentic audit, up to \$$cap)"
		"$D" audit -mode agentic -ledger "$L/ledger.json" -instance-config "$INST" -repo-name "$NAME" -format none . || true
		[[ -s "$L/ledger.json" ]] || { summary "docs-platform: the first audit wrote no ledger"; return 0; }
		echo "ledger=changed" >>"$GITHUB_OUTPUT"
	else
		instance "$DOCSP_RUN_CAP"
		"$D" stale -ledger "$L/ledger.json" -repo-name "$NAME" -format text . | tail -1 | tee -a "$GITHUB_STEP_SUMMARY"
		"$D" schedule -ledger "$L/ledger.json" -instance-config "$INST" -state "$L/state.json" -repo-name "$NAME" \
			-format none -out "$RUNNER_TEMP/schedule.json" .
		if [[ "$(jq '.picked | length' "$RUNNER_TEMP/schedule.json")" -gt 0 ]]; then
			"$D" recheck -schedule "$RUNNER_TEMP/schedule.json" -ledger "$L/ledger.json" -instance-config "$INST" \
				-state "$L/state.json" -repo-name "$NAME" -format text . | tail -1 | tee -a "$GITHUB_STEP_SUMMARY"
		fi
		# saved every run: the weekly run also keeps the cache from expiring
		echo "ledger=changed" >>"$GITHUB_OUTPUT"
	fi
	"$D" report -ledger "$L/ledger.json" -instance-config "$INST" -state "$L/state.json" -repo-name "$NAME" -format json . \
		>"$RUNNER_TEMP/report.json" || true
	jq -r '"docs-platform: \(.immediate | length) problems to report now, \(.batched | length) batched"' \
		"$RUNNER_TEMP/report.json" 2>/dev/null | tee -a "$GITHUB_STEP_SUMMARY" || true
}

pr_task() {
	if [[ "$DOCSP_FORK" == true || "$DOCSP_AI" != true ]]; then
		summary "docs-platform: no AI for this PR"
		return 0
	fi
	if [[ ! -s "$L/ledger.json" ]]; then
		summary "docs-platform: the default branch has no ledger yet (built by the first push run): nothing to compare"
		return 0
	fi
	instance "$DOCSP_RUN_CAP"
	cp "$L/ledger.json" "$RUNNER_TEMP/after.json"
	"$D" schedule -ledger "$RUNNER_TEMP/after.json" -instance-config "$INST" -repo-name "$NAME" \
		-format none -out "$RUNNER_TEMP/schedule.json" .
	# only what this change made stale (priority 0): due and quota claims are the default branch's business
	jq '.picked |= map(select(.priority == 0))' "$RUNNER_TEMP/schedule.json" >"$RUNNER_TEMP/schedule-pr.json"
	local n
	n="$(jq '.picked | length' "$RUNNER_TEMP/schedule-pr.json")"
	summary "docs-platform: $n claims made stale by this PR"
	if [[ "$n" -gt 0 ]]; then
		"$D" recheck -schedule "$RUNNER_TEMP/schedule-pr.json" -ledger "$RUNNER_TEMP/after.json" -instance-config "$INST" \
			-repo-name "$NAME" -format text . | tail -1 | tee -a "$GITHUB_STEP_SUMMARY"
	fi
	"$D" pr-comment -before "$L/ledger.json" -after "$RUNNER_TEMP/after.json" -repo-name "$NAME" \
		-github-repo "$GITHUB_REPOSITORY" -post-pr "$DOCSP_PR" . | tee -a "$GITHUB_STEP_SUMMARY"
}

factory_pr_task() {
	if [[ "$DOCSP_FORK" == true || "$DOCSP_AI" != true ]]; then
		summary "docs-platform: no AI for this PR"
		return 0
	fi
	# shellcheck disable=SC2086 # the factory command is a word list
	if ! $DOCSP_FACTORY export --out "$RUNNER_TEMP/manifest.json"; then
		summary "docs-platform: this head has no factory manifest (factory export failed): skipped"
		return 0
	fi
	instance "$DOCSP_RUN_CAP"
	"$D" factory-pr -base "$DOCSP_BASE" -factory-manifest "$RUNNER_TEMP/manifest.json" -instance-config "$INST" \
		-factory "$DOCSP_FACTORY" -repo-name "$NAME" -github-repo "$GITHUB_REPOSITORY" -post-pr "$DOCSP_PR" \
		-out "$RUNNER_TEMP/factory-pr.json" . | tee -a "$GITHUB_STEP_SUMMARY"
}

spend() {
	local usd=0
	[[ -s "$DOCSP_SPEND_LOG" ]] && usd="$(awk '{s += $2} END {printf "%.4f", s + 0}' "$DOCSP_SPEND_LOG")"
	summary "docs-platform: this run spent \$$usd"
	if awk -v u="$usd" 'BEGIN {exit !(u > 0)}'; then
		echo "name=docsp-usd-$MONTH-$usd-$DOCSP_TASK-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT" >>"$GITHUB_OUTPUT"
	fi
}

case "${1:-}" in
budget) budget ;;
main) main_task ;;
pr) pr_task ;;
factory-pr) factory_pr_task ;;
spend) spend ;;
*) echo "usage: run.sh budget|main|pr|factory-pr|spend" >&2; exit 2 ;;
esac
