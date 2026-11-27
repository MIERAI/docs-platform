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

INCOMPLETE="incomplete: this does not mean everything is fine"

# label_skip: the pull request carries the skip label (DOCSP_LABELS is the JSON array of its
# label names; label names come from the event, never from a shell word).
label_skip() {
	[[ -n "${DOCSP_SKIP_LABEL:-}" && -n "${DOCSP_LABELS:-}" ]] || return 1
	jq -e --arg l "$DOCSP_SKIP_LABEL" 'index($l) != null' <<<"$DOCSP_LABELS" >/dev/null 2>&1
}

# pr_outputs: the flags of what a PR check writes besides its comment.
pr_outputs() {
	PR_OUT=(-language "${DOCSP_LANGUAGE:-zh}" -max-problems "${DOCSP_MAX_PROBLEMS:-10}"
		-summary-url "${GITHUB_SERVER_URL:-https://github.com}/$GITHUB_REPOSITORY/actions/runs/${GITHUB_RUN_ID:-0}"
		-summary-out "$RUNNER_TEMP/docsp-summary.md" -annotations-out "$RUNNER_TEMP/docsp-annotations.txt")
}

# finish_pr: the full list into the job summary, a warning annotation on every problem line,
# and the check's conclusion: neutral (the step passes) unless DOCSP_CONCLUSION is failure.
finish_pr() { # $1 = problems found
	if [[ -s "$RUNNER_TEMP/docsp-summary.md" ]]; then
		cat "$RUNNER_TEMP/docsp-summary.md" >>"$GITHUB_STEP_SUMMARY"
	fi
	if [[ -s "$RUNNER_TEMP/docsp-annotations.txt" ]]; then
		cat "$RUNNER_TEMP/docsp-annotations.txt"
	fi
	if [[ "${DOCSP_CONCLUSION:-neutral}" == failure && "$1" -gt 0 ]]; then
		summary "docs-platform: $1 problems and conclusion: failure: the check fails"
		return 1
	fi
	return 0
}

# reserved_usd: what the other runs of this workflow still in progress may spend - each is
# counted at its task's default cap (push/schedule: the main cap, else the PR cap). A run's
# spend is recorded only when it ends, so without this concurrent runs (several PRs pushed
# at once) each see the whole remainder and together overrun the month. Runs that start at
# the same moment see each other, so the error is towards spending less, never more.
reserved_usd() {
	local wf="${GITHUB_WORKFLOW_REF:-}" main_cap=3 pr_cap=1
	wf="${wf#*/.github/workflows/}"
	wf="${wf%%@*}"
	[[ -n "$wf" ]] || { echo 0; return 0; }
	[[ -n "${DOCSP_RUN_USD:-}" ]] && main_cap="$DOCSP_RUN_USD" && pr_cap="$DOCSP_RUN_USD"
	local st
	for st in in_progress queued; do
		gh api "repos/$GITHUB_REPOSITORY/actions/workflows/$wf/runs?status=$st&per_page=100" \
			--jq ".workflow_runs[] | select(.id != ${GITHUB_RUN_ID:-0}) | .event" 2>/dev/null || true
	done | awk -v m="$main_cap" -v p="$pr_cap" 'NF {s += ($1 == "push" || $1 == "schedule" || $1 == "workflow_dispatch") ? m : p} END {printf "%.4f", s + 0}'
}

budget() {
	local ai=true cap
	if [[ "$DOCSP_TASK" != main ]] && label_skip; then
		ai=false; summary "docs-platform: the pull request has the label $DOCSP_SKIP_LABEL: not checked"
	fi
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
	local left reserved
	reserved="$(reserved_usd)"
	left="$(awk -v m="$DOCSP_MONTHLY" -v s="$spent" -v r="$reserved" 'BEGIN {printf "%.4f", m - s - r}')"
	if awk -v r="$reserved" 'BEGIN {exit !(r > 0)}'; then
		summary "docs-platform: \$$reserved reserved for other runs of this workflow in progress"
	fi
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

# outcomes: the precision of docs-platform's own reports (docsp outcome; no model). The stream
# lives next to the ledger (saved with its cache) and is uploaded as an artifact, which
# outlives a cache eviction and is fetched back when the cache comes without it. Never fails
# the job.
restore_outcomes() {
	[[ -s "$L/outcomes.jsonl" ]] && return 0
	local url
	url="$(gh api "repos/$GITHUB_REPOSITORY/actions/artifacts?name=docsp-outcomes&per_page=10" \
		--jq '[.artifacts[] | select(.expired | not)][0].archive_download_url // empty' 2>/dev/null)" || return 0
	[[ -n "$url" ]] || return 0
	gh api "$url" >"$RUNNER_TEMP/outcomes.zip" 2>/dev/null &&
		unzip -o -q "$RUNNER_TEMP/outcomes.zip" outcomes.jsonl -d "$L" || true
}

outcomes() { # $1 = the ledger at the start of the run
	local s="$L/outcomes.jsonl" pr
	if [[ -s "$L/ledger.json" ]]; then
		"$D" outcome diff -before "$1" -after "$L/ledger.json" -repo-name "$NAME" -stream "$s" . || true
	fi
	# the boxes ticked on the pull request(s) this push merged
	if [[ "${GITHUB_EVENT_NAME:-}" == push ]]; then
		for pr in $(gh api "repos/$GITHUB_REPOSITORY/commits/$GITHUB_SHA/pulls" --jq '.[].number' 2>/dev/null || true); do
			"$D" outcome ticks -github-repo "$GITHUB_REPOSITORY" -pr "$pr" -ref "$GITHUB_SHA" -repo-name "$NAME" \
				-ledger "$L/ledger.json" -stream "$s" . || true
		done
	fi
	[[ -s "$s" ]] && { "$D" outcome report -stream "$s" -format markdown >>"$GITHUB_STEP_SUMMARY" || true; }
	return 0
}

main_task() {
	mkdir -p "$L"
	restore_outcomes || true
	cp "$L/ledger.json" "$RUNNER_TEMP/ledger-before.json" 2>/dev/null || rm -f "$RUNNER_TEMP/ledger-before.json"
	if [[ "$DOCSP_AI" != true ]]; then
		summary "docs-platform: no AI this run: the ledger is kept as it is"
		# ticks still count; the cache is saved only over a ledger, never instead of one
		if [[ -s "$L/ledger.json" ]]; then
			outcomes "$RUNNER_TEMP/ledger-before.json"
			echo "ledger=changed" >>"$GITHUB_OUTPUT"
		fi
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
	outcomes "$RUNNER_TEMP/ledger-before.json"
}

pr_task() {
	if label_skip; then
		summary "docs-platform: the pull request has the label $DOCSP_SKIP_LABEL: not checked"
		return 0
	fi
	if [[ "$DOCSP_FORK" == true || "$DOCSP_AI" != true ]]; then
		summary "docs-platform: no AI for this PR: not checked ($INCOMPLETE)"
		return 0
	fi
	if [[ ! -s "$L/ledger.json" ]]; then
		ledgerless_pr
		return
	fi
	instance "$DOCSP_RUN_CAP"
	cp "$L/ledger.json" "$RUNNER_TEMP/after.json"
	local n=0 incomplete="" args=()
	if "$D" schedule -ledger "$RUNNER_TEMP/after.json" -instance-config "$INST" -repo-name "$NAME" \
		-format none -out "$RUNNER_TEMP/schedule.json" .; then
		# only what this change made stale (priority 0): due and quota claims are the default branch's business
		jq '.picked |= map(select(.priority == 0))' "$RUNNER_TEMP/schedule.json" >"$RUNNER_TEMP/schedule-pr.json"
		n="$(jq '.picked | length' "$RUNNER_TEMP/schedule-pr.json")"
		args+=(-schedule "$RUNNER_TEMP/schedule-pr.json")
	else
		incomplete=aborted
	fi
	summary "docs-platform: $n claims made stale by this PR"
	if [[ "$n" -gt 0 ]]; then
		if ! "$D" recheck -schedule "$RUNNER_TEMP/schedule-pr.json" -ledger "$RUNNER_TEMP/after.json" -instance-config "$INST" \
			-repo-name "$NAME" -format text -out "$RUNNER_TEMP/recheck.json" . | tail -1 | tee -a "$GITHUB_STEP_SUMMARY"; then
			incomplete=aborted
		fi
		if [[ -s "$RUNNER_TEMP/recheck.json" ]]; then
			args+=(-recheck "$RUNNER_TEMP/recheck.json")
		else
			incomplete=aborted
		fi
	fi
	if [[ -n "$incomplete" ]]; then
		summary "docs-platform: $INCOMPLETE (a step of the check failed)"
	fi
	pr_outputs
	if ! "$D" pr-comment -before "$L/ledger.json" -after "$RUNNER_TEMP/after.json" -repo-name "$NAME" \
		-github-repo "$GITHUB_REPOSITORY" -post-pr "$DOCSP_PR" -incomplete "$incomplete" -out "$RUNNER_TEMP/pr-review.json" \
		${args[@]+"${args[@]}"} "${PR_OUT[@]}" . | tee -a "$GITHUB_STEP_SUMMARY"; then
		summary "docs-platform: $INCOMPLETE (the PR comment could not be made)"
		return 1
	fi
	finish_pr "$(jq '.problems | length' "$RUNNER_TEMP/pr-review.json")"
}

# Zero-config PR mode: no ledger yet (no push run, or its cache expired). The sections the PR
# edits or relates to (headings + identifier overlap with the diff, deterministic) are judged
# at the head within the run's cap, and compared with an empty ledger: every problem found in
# them is reported. A push run (task main) builds the ledger, the upgrade path.
ledgerless_pr() {
	instance "$DOCSP_RUN_CAP"
	if ! "$D" related -base "$DOCSP_BASE" -out "$RUNNER_TEMP/related.json" -format text . >"$RUNNER_TEMP/related.txt"; then
		summary "docs-platform: no diff against the PR's base (a shallow checkout? actions/checkout needs fetch-depth: 0): skipped ($INCOMPLETE)"
		return 0
	fi
	tee -a "$GITHUB_STEP_SUMMARY" <"$RUNNER_TEMP/related.txt"
	local n incomplete=""
	n="$(jq '.sections | length' "$RUNNER_TEMP/related.json")"
	summary "docs-platform: no ledger yet: judging the $n doc sections this PR is about (ledger-less mode, up to \$$DOCSP_RUN_CAP)"
	if [[ "$n" -gt 0 ]]; then
		if ! "$D" audit -mode agentic -group doc -no-drift -sections "$RUNNER_TEMP/related.json" -max-questions 40 \
			-ledger "$RUNNER_TEMP/after.json" -instance-config "$INST" -repo-name "$NAME" -format none .; then
			incomplete=aborted
			summary "docs-platform: $INCOMPLETE (the ledger-less audit failed)"
		fi
	fi
	pr_outputs
	if ! "$D" pr-comment -before "$RUNNER_TEMP/no-ledger.json" -after "$RUNNER_TEMP/after.json" -repo-name "$NAME" \
		-github-repo "$GITHUB_REPOSITORY" -post-pr "$DOCSP_PR" -ledgerless -incomplete "$incomplete" \
		-out "$RUNNER_TEMP/pr-review.json" "${PR_OUT[@]}" . | tee -a "$GITHUB_STEP_SUMMARY"; then
		summary "docs-platform: $INCOMPLETE (the PR comment could not be made)"
		return 1
	fi
	finish_pr "$(jq '.problems | length' "$RUNNER_TEMP/pr-review.json")"
}

factory_pr_task() {
	if label_skip; then
		summary "docs-platform: the pull request has the label $DOCSP_SKIP_LABEL: not checked"
		return 0
	fi
	if [[ "$DOCSP_FORK" == true || "$DOCSP_AI" != true ]]; then
		summary "docs-platform: no AI for this PR: not checked ($INCOMPLETE)"
		return 0
	fi
	# the project's go.mod may want a newer Go than the runner's (setup-go sets GOTOOLCHAIN=local)
	export GOTOOLCHAIN=auto
	# shellcheck disable=SC2086 # the factory command is a word list
	if ! $DOCSP_FACTORY export --out "$RUNNER_TEMP/manifest.json"; then
		summary "docs-platform: this head has no factory manifest (factory export failed): skipped"
		return 0
	fi
	instance "$DOCSP_RUN_CAP"
	pr_outputs
	if ! "$D" factory-pr -base "$DOCSP_BASE" -factory-manifest "$RUNNER_TEMP/manifest.json" -instance-config "$INST" \
		-factory "$DOCSP_FACTORY" -repo-name "$NAME" -github-repo "$GITHUB_REPOSITORY" -post-pr "$DOCSP_PR" \
		-out "$RUNNER_TEMP/factory-pr.json" "${PR_OUT[@]}" . | tee -a "$GITHUB_STEP_SUMMARY"; then
		summary "docs-platform: $INCOMPLETE (the factory review failed)"
		return 1
	fi
	finish_pr "$(jq '[.rules[] | select(.category == "contradicted" or .category == "outdated_wording" or .category == "incomplete_list" or .category == "dangling_reference")] | length' "$RUNNER_TEMP/factory-pr.json")"
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
