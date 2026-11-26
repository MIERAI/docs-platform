# Lab notebook: how the model layer got its shape

What was tried for layer ④c (checking documentation claims against code with a model), what
it scored, and why the design is what it is. Chronological, 2026-11-25 to 2026-11-26.

**Sources.** Numbers come from files: the frozen sets and answers in `bench/data` (public,
reproducible with `make repro-offline`), the eval/v1 results of the paid product runs, and
the experiment archive of the prototypes (private: Python scripts, run logs and per-item
results, `agentic-exp/runs/{dev,test,audit}`, `v2_spend.jsonl`, `v3_spend.jsonl`,
`ledger-exp/`). Each row names its source. *Design* means [design.md](design.md), where
a number is stated there but no raw file backs it; such numbers are marked **(design only)**.

**Reading the numbers.** DEV = api-factory (30 positives + 29 controls); TEST = gitops-manifests
+ infrastructure-live (30 + 30), held out: designs were tuned on DEV and run once on TEST.
Positives are ground-truth defects from one strong-AI audit, so recall is *agreement with
that audit*. One item is 3.3 points; differences of one or two items are noise.

## 1. Snippet judge: the model gets three snippets and answers yes/no (27%)

The first model layer (v0.2/v0.3) asked one question per claim with at most three code
snippets retrieved deterministically, and required a verbatim quote.

| Run | Set | Recall | Controls flagged | Cost | Source |
|---|---|---|---|---|---|
| gemma4:12b (local), whole semantic audit, model sub-layer | TEST, 73 GT items | 0/73 | - | $0 | design §9.1 **(design only)** |
| Sonnet 5.5, snippet, 10-item pilot | gitops-manifests | 2/5 stale, 5/5 controls unknown | 0 | $0.44 | `reports/pilot-sonnet-10.json` |
| Sonnet 5.5, snippet ("constrained") | TEST | **8/30 (27%)** | 0/30 | $0.92 | `runs/test/s_constrained.jsonl` |
| Sonnet 5.5 in gemma's best agentic setup (g26) | TEST | 8/30 (27%), 26 unknown | 0/30 | $2.42 | `runs/test/s_agentic_g26.jsonl` |

A strong model scored no better than a weak one under the same question format: the
bottleneck was the platform (what the model was shown and allowed to say), not the model.

## 2. Weak local model, agentic: structure beats tools (gemma4:12b, DEV)

The model explores the repository with read-only tools. Configurations in `configs.json`;
all with thinking off, 12 steps, 16k context.

| Config | Change | Recall | Controls flagged | Source |
|---|---|---|---|---|
| g01 native_base | baseline tool loop | 3/30 (10%) | 0/29 | `runs/dev/g01_native_base.jsonl` |
| g02 native_calib | calibrated prompt | 3/30 (10%) | 0/29 | `g02` |
| g09 native_hunt | "hunt for the mismatch" prompt | 3/30 (10%) | 0/29 | `g09` |
| g12 twostage_hunt | two stages | 0/30 | 0/29 | `g12` |
| g13 native_fact_think | thinking on | 2/21 (incomplete run) | - | `g13` |
| **g11 native_fact** | **split the line into facts, check each** | **7/30 (23%)** | 0/29 | `g11` |
| g21 fact | repeat of g11 | 7/30 | 0/29 | `g21` |
| g17 fact + few-shot | | 7/30 | 0/29 | `g17` |
| g22 fact + helper tools | changes_since, find_references, config_value, outline, check_quote | 7/30 | 0/29 | `g22` |
| g24 helpers, LSP references | | 9/30 | 1/29 | `g24` |
| g25 helpers, tree-sitter outline | | 7/30 | 0/29 | `g25` |
| g26 helpers, ripgrep | | 9/30 | 0/29 | `g26` |
| g27 helpers, ast-grep | | 6/30 | 0/29 | `g27` |
| g23 helpers, changes_since pre-filled | | 2/30 (7%) | 0/29 | `g23` |

Findings:

- **Fact-by-fact structure doubled recall** (3/30 → 7/30) at no extra cost: the model checks
  "named path X exists", "host is Y" one at a time instead of judging a sentence.
- **Helper tools and alternative backends were within noise** (6-9/30 around 7/30). They
  stayed in the product (`outline`, `config_value`, `changes_since`, ...) because they save
  calls, not because they raised recall. Pre-filling the helper's output into the prompt
  collapsed recall (g23).
- Thinking made the weak model worse and slower (g13); the owner decided: no thinking for
  local models.
- Unknowns dominate (27-45 of 59): a weak model rarely commits.

## 3. v2 redesign on Sonnet: 27% → 80%

Tuned on DEV, run once on TEST (`v2.py`). Five changes, all kept in the product (§3.8):
(1) a **repository drift scan** once per repository: the model lists history changes that
make older docs wrong, the harness keeps only entries whose commits, quotes and removed
paths check out; (2) **six verdict categories** instead of yes/no (contradicted,
incomplete_list, outdated_wording, dangling_reference, unverifiable_in_repo, confirmed);
(3) **evidence re-checked by the harness** (quote at path:line±2, absence by re-running the
exact search, drift id); (4) **structured output** bound to a JSON Schema; (5) fact-by-fact
answers, with the verdict derived by the harness, not the model.

| Run | Set | Recall | wrong+obsolete / missing | Controls flagged | Unknown | $/line | Source |
|---|---|---|---|---|---|---|---|
| v2 line | DEV | 18/30 (60%) | 16/22 / 2/8 | 1/29 | 2 | $0.041 | `runs/dev/v2_line.jsonl` |
| v2 line, no drift list | DEV | 18/30 | 16/22 / 2/8 | 1/29 | 4 | $0.035 | `v2_line_nodrift` |
| v2 doc (≤6 lines per request) | DEV | 16/30 | 13/22 / 3/8 | 2/29 | 2 | $0.020 | `v2_doc` |
| **v2 line, frozen** | **TEST** | **24/30 (80%)** | 19/23 / 5/7 | 4/30 | 6 | $0.039 | `runs/test/v2_line_FROZEN.jsonl` |
| drift scans | | 18/18, 15/15, 12/16 validated | | | | $0.16 / $0.25 / $0.14 per repo | `drift/*.json` |

Of the 4 flagged TEST controls, adjudication found 1 real false positive **(design only,
§3.8)**; the rest were real drift the controls had been assumed free of.

On DEV the drift list did not change recall (it reduced unknowns); on TEST several
positives depend on drift entries (section 4). Per-document grouping halves the cost and
loses about two items (DEV); line grouping stays the default.

Local models in the v2 design, judging with Sonnet's drift lists ("hybrid"), TEST:
gemma4:12b 15/30 with 0/30 controls flagged and 20 unknown, about 85 s per line;
qwen3.6:35b 22/30 but 12/30 controls flagged (`runs/test/v2L_{gemma,qwen}_hybrid.jsonl`).
In the v2 design the weak model reaches 50% (15/30) without flagging a control: more than
Sonnet scored in the snippet design (27%).

`make repro-offline` re-scores the frozen TEST answers through the product: 24/30, precision
0.86 (4/30 controls), 18/18 and 15/15 drift entries validated; DEV 18/30, 1/29.

## 4. Porting the prototype: three regressions, then 73% / 77%

Paid TEST runs of the product (Claude Code CLI, Sonnet 5.5), eval/v1 results:

| Version | Recall | Precision | Mean tool steps / line | Drift validated | Nominal cost | Result file |
|---|---|---|---|---|---|---|
| prototype | 24/30 (0.80) | 0.86 | 5.4 **(design only)**; 4.7 turns | 18/18, 15/15 | $2.32 + drift | `v2_line_FROZEN` |
| 0.4.0 | 22/30 (0.73) | 0.85 | 2.8 **(design only)** | 21/24, 12/14 | $3.48 | `...20261125T234318Z.json` |
| 0.4.1 | 19/30 (0.63) | 0.73 | 3.0 | 22/22, 17/17 | $3.40 | `...20261125T235904Z.json` |
| **0.4.2** run 1 | **22/30 (0.73)** | 0.85 | 2.9 | 23/23, 18/18 | $3.33 | `...20261126T000956Z.json` |
| **0.4.2** run 2 | **23/30 (0.77)** | 0.82 | 2.8 | 23/23, 18/18 | $2.74 | `...20261126T001345Z.json` |

What broke and how it was fixed (CHANGELOG 0.4.1, 0.4.2):

1. **No line numbers** (0.4.0). `read_file` returned an unnumbered JSON array; the model
   counted lines, quoted the right text at the wrong line, and the harness rejected it.
   All 5 rejected drift entries of the run had the right text 4-29 lines off, including
   two that lost positives depended on. Fix: tool results reach the model as plain text,
   `12: text` and `path:line: text`, like its own file tools.
2. **JSON escaping** (0.4.0). Go's encoder turned `<`, `>`, `&` into `<` etc., so no
   line containing them could be quoted verbatim. Fix: no HTML escaping; lines cut at 2000
   characters instead of 400.
3. **Inherited effort** (0.4.1, recall 0.63). Agentic runs inherited the snippet setting
   `effort: medium`; the prototype passed none (the model's default). Together with an
   appended "reply with one JSON object only" sentence, a missing repository context (the
   prototype's CLI ran inside the checkout) and three drifted prompt sentences. Fix: agentic
   effort is separate (`ai.judge.agentic.effort`, default unset), only the untrusted-data
   rule is appended, the repository context is stated explicitly, the prototype's text is
   restored verbatim.

0.4.2 recovered 73-77% over two runs (one item apart) and became the baseline. The mean
tool steps per line stayed at 2.8-2.9 (prototype 4.7 turns): recall came back without the
model exploring more, so "stopping early" was not the main cause of the 0.4.1 drop.

The method that found these: replay the prototype's frozen answers through the product
first (free, deterministic: 60/60 verdicts equal, design §3.8), compare byte by byte what each side sent
the model, and only then pay for a rerun; pay for at least two.

## 5. Whole-document audit: the model also picks the lines (40%)

Instead of judging given lines, Sonnet reads a whole document, proposes candidate claims
(about one per 5-8 lines), and the v2 judge checks them (`v3.py`, DEV, api-factory, scored
against the ground-truth lines of the same documents, ±1 line):

| Run | Documents | GT lines found | Candidates | Flags | Flags not in GT | Cost | Source |
|---|---|---|---|---|---|---|---|
| a1 | 7 | **20/50 (40%)** | 172 | 44 | 23 | $2.59 | `runs/audit/a1_dev` |
| a2 (more candidates) | 7 | 21/50 (42%) | 221 | 48 | 24 | $3.46 | `runs/audit/a2_dev` |
| a3 | 3 | 14/33 (42%) | 111 | 24 | 8 | $2.14 | `runs/audit/a3_dev` |

More candidates did not find more: line selection, not judging, limits recall. Unmatched
flags were not triaged (some are real drift outside the ground truth). This line of work
stays an experiment (design §12.6); its interface to the product is the claim ledger.

The mutation set (`go run ./bench mutate`, 47 code mutations in two repositories, 74
affected documentation lines, 65 distinct) was built for this audit on known, injected
drift. No scored mutation run exists in the archive.

## 6. Keeping docs true over time: ledger replay 76% / 92% (optimistic)

Judging once and recording the validated evidence of each verdict (blobs of quoted files,
absence-search scopes, missing paths) lets a later commit invalidate exactly the claims
whose evidence changed, without a model (§12). Offline validation (`bench ledger-replay`,
ledger = the frozen TEST answers replayed at the TEST revisions, 60 claims):

| | First causing commit | Any causing commit | Source |
|---|---|---|---|
| ledger invalidation | **19/25 (76%)** | **23/25 (92%)** | `make repro-offline` (= `ledger-exp/ledger-replay.json`) |
| change-time impact selection, same 25 items | 9/25 (36%) | 13/25 (52%) | `make repro-offline` (`bench verify-offline` joins the two replays) |

Cost: gitops-manifests 329 commits, 0.66 claims staled per commit (p90 2, max 11), 275
commits (83%) stale nothing; infrastructure-live 50 commits, 0.80 per commit, 32 (64%) stale
nothing.

**Why optimistic.** The evidence footprints come from answers given at the TEST revision,
which cite exactly the files that show the line is stale, and causing commits are defined
by the ground truth's evidence paths; the two overlap by construction. A ledger built when
the line was written would cite the evidence that confirmed it then. The honest test (a
ledger built at earlier revisions, `bench ledger-backtest`) needs paid runs; `ledger-exp/bt`
holds its preparation (revision windows, per-window sets); the archive had no paid result
when this was written.

## 7. Why the design is what it is

- **Deterministic first, models behind a contract.** History replay (52% / 67% on TEST
  with no model) and the ledger carry the load; the model is a swappable judge
  (`judge/v1`), switched by instance configuration and scored on frozen sets.
- **Give the model the repository, not snippets** (sections 1, 3), with the history's drift
  as shared context.
- **Never trust the model's evidence**: the harness re-checks every quote, search and path
  and derives the verdict. Controls stay quiet: 1/29 flagged on DEV; 4/30 on TEST, of which
  1 was a real false positive after adjudication.
- **Ports are verified offline before paid reruns**, and paid reruns are done twice.
- **Strong model now, weak model later**: the local model reaches 50% in the v2 design with
  strong-model drift lists (section 3); replacing the judge is a configuration change
  evaluated on the same frozen set.
