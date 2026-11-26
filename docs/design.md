# docs-platform 设计书

状态：v0.4.2 + 未发布的 §12（层③ 引用检查、层④a 变更时影响推导、层④c 语义审计（snippet 与 agentic 两种判定模式）、模型契约 judge/v1 1.2 + 5 种 provider + 按任务路由 + 数据分级、模型评测、历史回放基准、积压导入已实现；其余为设计）
读者：产品负责人（本人）。面向用户的说明见 [README](../README.md)，契约细节见 [contracts.md](contracts.md)。

---

## 0. 一句话

**把"文档与代码一致"当成可靠性问题来运营**：确定性检测器发现漂移 → 产出与 security-platform 同一契约的 findings → 台账（ledger）跟踪生命周期 → SLO 量化（漂移多久修好、影响文档的 PR 有多少带了文档）→ 修复闭环。AI 只提议，人决定。

## 1. 问题

1. 文档腐烂是静默的：flag 改名、环境变量删掉、脚本搬家，README 仍描述旧世界，直到新人照着做失败。
2. 人工审阅不可持续：本次基准里 3 个仓库人工审出 147 条问题，平均每条需要读文档 + 翻代码对照；这件事每个 PR 都该做，人做不过来。
3. 现有工具只覆盖一角：链接检查器只看链接；`--help` 生成器只覆盖 CLI；没有工具把"文档里提到的一切标识符"和"代码里定义的一切公开符号"双向对账，更没有台账和 SLO。
4. 漂移的种类差别很大，必须分层：有些是**确定可判定**的（提到的文件不存在），有些需要**理解语义**（"每次运行都会重启 k3s"其实只在某个 tag 下才重启）。混在一起做，要么误报多到没人看，要么只能全交给 AI 而失去可重复性。

## 2. 原则（照搬产品族的共同原则）

| 原则 | 在本产品中的落地 |
|---|---|
| 通用核心 + 插件 | 核心只认识 Symbol / Reference / Mention / Finding；语言、文档格式、代码托管、AI 厂商都是插件（`plugin.Extractor`、外部抽取器、`ai.Provider`） |
| 版本化、语言中立的契约 | `contracts/` 下的 JSON Schema：findings/v1（与 security-platform 共用）、extract/v1、impact/v1、config/v1、judge/v1、judge-tools/v1、instance/v1、eval/v1；各部件只经契约耦合 |
| 各部件独立升级 | 每个抽取器有自己的 name/version，写入 findings 的 `collector.tools`；外部抽取器是独立进程，任意语言 |
| 产品仓库不含环境信息 | 仓库里没有任何主机、仓库名、人名；`related_repos`、denylist、基准用的实例配置都在仓库外 |
| "不优秀就活不长" | golangci-lint 严格配置（函数 ≤50 行、认知复杂度、gosec、wrapcheck…）、表驱动测试、每个外部输入解析器都有 fuzz、golden 测试、契约测试、govulncheck；核心唯一第三方依赖 yaml（Anthropic SDK 在独立模块 `providers/anthropic`） |
| 平台自身安全 | 对被检仓库只读（`os.Root` 防越界读、git 子命令白名单、无 shell、无执行仓库内容）；私密文件只读 0600；输出文件 0600 |
| AI 只提议、模型可换 | 模型只经 judge/v1 契约接入；provider/模型/预算只来自实例配置，换模型 = 改配置；默认 `none`；判定与草稿都是提议；状态变更（关闭/接受 finding、合并 PR）只有人能做 |

## 3. 四层检测

| 层 | 检什么 | 判定方式 | 何时跑 | 本轮 |
|---|---|---|---|---|
| ① 生成的参考文档 | CLI 帮助、配置项表、指标表、环境变量表由代码生成，CI 比对 | 确定性（diff） | 每个 PR | 设计 |
| ② 可执行文档 | 文档里的 shell 代码块在 CI 沙箱里真跑 | 确定性（退出码/输出） | 每个 PR / 每夜 | 设计 |
| ③ 引用检查 | 文档提到的标识符必须存在；公开符号必须有文档 | 确定性（符号表对账）+ 置信度 | 每个 PR + 定期 | **已实现** |
| ④ 语义 | (a) 变更时影响：从引用图推导该重读的段落 (b) 模型判定"这次变更是否与该段矛盾"+ 草拟修改 (c) 定期语义审计 → findings | (a) 确定性；(b) 模型提议 + 人审；(c) 确定性语义检查 + 可验证的模型小问题 + 人审 | PR / 合并后 / 每周 | **(a)(b)(c) 已实现**（c 有 snippet 与 agentic 两种模式；agentic 见 §3.8） |

分层的理由：①②③④a 是确定性的，可以做 CI 闸门；④b/④c 只能产出"提议"，进台账但不阻断。基准结果（§9）显示 ~85% 的人工审出问题属于语义层，这正是为什么 ④ 不能省，也是为什么 ③ 必须把误报压低——否则闸门会被关掉。

### 3.1 层① 生成的参考文档（设计）

<!-- docsp:ignore-start 计划中的命令，层① 实现后删除本标记 -->

- 生成器是插件：`generator` 读 extract/v1（已有的符号表！）渲染 Markdown 片段，写在文档里一对标记之间：`<!-- docsp:generated kind=cli_flag program=docsp -->` … `<!-- docsp:end -->`。
- CI：`docsp generate --check` 重新渲染，与文件内容不同即失败（finding `docs.generated.stale`）。开发者本地 `docsp generate --write`（唯一会写文件的命令，只写调用者自己的工作区，不在 CI 的检测路径上）。
- 好处：层③ 的"未文档化"问题对生成区域天然消失；人写的叙述部分仍由层③ 检查。

<!-- docsp:ignore-end -->

### 3.2 层② 可执行文档（设计）

- 标注：围栏代码块的 info string 加 `docsp-run`（例：` ```sh docsp-run timeout=60s `）。不标注的不跑——默认不执行任何东西。
- 执行器是插件，**只在一次性容器里跑**（无网络、只读挂载仓库副本、资源上限），绝不在开发者机器或 CI runner 宿主上直接跑。输出可选 `docsp-expect` 块做比对。
- finding：`docs.executable.failed`，证据为命令、退出码、截断输出。

### 3.3 层③ 引用检查（已实现）

流程：

```
仓库(只读) ──▶ 抽取器插件 ──▶ Inventory{symbols, references, mentions, suppressions, code tokens, gaps}
                                  │
                        matcher（index + resolve + reverse）
                                  │
             Issues ──▶ findings/v1 文档（+ docs_ 指标）──▶ ledger / scorecard / 看板
```

- **正向（文档 → 代码）**：每条 Reference（种类：cli_command / cli_flag / env_var / config_key / metric / file_path / link / anchor / make_target）解析为 resolved / unresolved / external / outside / ignored。unresolved = finding `wrong_or_obsolete`。
- **反向（代码 → 文档）**：每个 public Symbol 若在任何文档的 token 中都没出现 = finding `undocumented`。
- **压低误报的手段**（每一条都来自基准里真实遇到的误报）：
  1. 置信度三级，决定严重度；CI 默认只看 medium 以上。
  2. 程序作用域：`kubectl apply --server-side` 的 flag 不属于本仓库 → external，不报。
  3. 代码 token 兜底：引用在代码里以字符串出现过就视为存在（但**去掉注释后**再建 token 索引，免得过时注释给自己作证）。
  4. 路径：`.gitignore`（含子目录的）匹配 = 生成物；`cd dir && ./x` 会把后续路径改写到 dir 下；`--out`/`>` 后面的路径是输出不检查；占位符路径（`x/Y.yaml`、`vX.Y.Z`）不检查；相对子目录的写法（`modules/net`）按后缀匹配。
  5. 跨仓库：`owner/repo path` 或实例配置 `related_repos` 中的仓库名紧跟在路径前 → 那是别的仓库的路径。
  6. 指标：只有落在本仓库指标命名空间（如 `tool_`）里的未知指标才报；`node_`、`kube_` 等导出器指标视为外部。
  7. 裸 flag（不跟在任何命令后面）只有在与本仓库某个 flag 编辑距离很近时才是 medium，否则 low。
  8. 反向检查：脚本/程序本身在文档里一次都没出现 = 内部工具，其参数降为 low。
  9. 抑制必须带理由：`.docsp.yaml` 的 ignore 规则（reason 必填）、文档内 `<!-- docsp:ignore <reason> -->`。
- **残留词检查（residue）**：实例私有的 denylist（0600 文件，不进产品仓库）列出"公开仓库里绝不能出现"的词：无关/私有项目名、内部主机名、个人路径。逐行、大小写不敏感、词边界匹配 → `docs.residue.term`。这是对"public 仓库提到外部项目名"这类人工审出问题的确定性兜底。
- **注释也是文档**：`comments` 抽取器把 Go/shell/Python/YAML/Terraform/JS 注释里的内联代码和路径当作文档引用检查（基准里 1/3 的问题出在注释里）。注释里的 token 不计入"文档已提及"（反向检查只认真正的文档）。

### 3.4 层④a 变更时影响（已实现：`docsp impact`）

产品负责人的方向（决定 B）：**在变更发生时发现漂移，而不是事后去找**；用历史回放来度量。

- **引用图**：层③ 已经知道每个文档段落提到了什么。段落 = Markdown 的标题段（到下一个任意级标题为止）或代码里一段连续注释。每段挂上它提到的 Reference（路径、环境变量、flag、命令、配置键、指标、make 目标、形似标识符的 term）。
- **推导**（`internal/impact/derive.go`，纯函数）：`git diff -U0 base...head` → 变更文件集合 + 变更行（增删两侧）上的标识符 token。段落命中条件：提到的路径与变更文件相等 / 相对文档目录相等 / 后缀相等 / 是其父目录 / 文件名相同（仅带扩展名的名字）；或提到的标识符出现在**另一个文件**的变更行上。输出 `sections[]`：`reread`（段落没改，需要重读）或 `updated`（这次变更已改了该段），附原因（"`APP_PORT` 在 `cmd/tool.go:5` 变了"）。**不需要手写映射。**
- **显式规则仍是闸门**：`.docsp.yaml` 的 `impact.rules`（code globs → docs globs）+ 带理由的 `Docs-Not-Needed` trailer 决定 pass/fail；规则命中的文档以整篇条目（原因 `rule`）加进段落列表。推导结果是建议性的，不改变 verdict——闸门必须可预测；推导的价值先由历史回放证明（§9.2），之后再决定是否升级为闸门。
- **模型（可选）**：实例配置里有 judge 时，对每个 `reread` 段落（上限 `--max-judge`，默认 20）问"这次变更是否让该段某句话错误/过时？"，证据 = 原因文件的增删行；判定 yes 且引用核验通过 → 交给 draft provider 起草修改。都只写进 impact/v1 报告，从不应用。
- 指标：`docs_impact_changes{impacting, with_docs}`（规则 SLI）+ `docs_impact_sections{status}`（每次变更要重读多少段，代价侧）。

### 3.5 层④b 模型层（已实现：契约 + provider + 路由 + 分级 + 评测）

产品负责人的决定 A：**目前判定用强模型（Claude），但模型部分必须随时可换**——本地模型够强时只改配置即可替换。设计：

1. **契约 judge/v1**（`contracts/judge-v1-*.schema.json`，语言中立、版本化）：请求 = 任务类型（`claim_contradiction` / `claim_incomplete` / `change_contradiction` / `draft_edit`）、断言、带 id 的证据片段（E1…）、必须的逐字引用、核心给定的**完整提示词**（版本 + 16 位哈希 + system + user + 回答的 JSON Schema，所有 provider 吃同一段文字，评测才公平）、模型、限额（剩余 USD/token：provider 必须拒绝而不是超支）、dry_run；1.1 增加 `mode: snippet | agentic` 与工具声明。回答 = verdict（yes/no/unknown）、quote、confidence、实际作答的模型 id、prompt_hash、token/成本计量（含 billing: none/api/subscription）、延迟、错误码；1.1 增加 agentic 的证据列表 `evidence[{path,line,quote,rev}]` 与工具调用记录 `transcript`。Go 类型在公开包 `judgev1`（只用标准库，外部 provider 可直接 import）。
2. **Provider 插件**（`internal/judge`）：`none`；`ollama`（本地，拨号时只允许本地地址）；`claude-code`（第一强模型选项：`claude -p` 非交互、argv 无 shell、`--tools ""`、无 MCP/settings/会话落盘、空临时目录、`--json-schema` 结构化输出；凭据完全依赖 CLI 自身登录或 CI 的 `CLAUDE_CODE_OAUTH_TOKEN`（按名透传），docsp 不读不打印不复制）；`anthropic`（第二选项：独立模块 `providers/anthropic` 的 `docsp-judge-anthropic`，官方 Go SDK，经 exec 协议接入，key 只来自 0600 文件或配置里点名的环境变量，HTTP 只允许 `https://api.anthropic.com`）；`exec`（任意讲 judge/v1 JSON Lines 的程序，任何未来的模型/运行时不改产品即可接入；默认视为云端，除非声明 `local: true`）。
3. **按任务路由**：实例配置 `ai.judge` 与 `ai.draft` 各自独立选择 provider/模型/预算，各自评测、各自切换。draft 只在 judge 判 yes 且引用核验通过之后才调用。仓库的 `.docsp.yaml` 不能选模型（`audit.ai` 现在是错误）。
4. **Guard**（所有 provider 共用）：数据分级（见 5）→ 预算（请求数/token/USD，发送前按"本请求最坏情况"预判，超出即硬停）→ 调用 → 用 judge/v1 schema 校验回答、核对 id → 计量 → 引用核验（snippet：在某一证据行里逐字出现、长度 ≥10、不是抄断言、与断言有共同词；agentic：每条证据在 worktree 的 path:line±2 找到）→ 核验失败的 yes 降为 unknown 并计入引用拒绝率。
5. **数据分级（安全）**：实例配置给每个仓库/路径 `public` / `private`（默认 private，路径规则可覆盖仓库）。云端 provider（claude-code、anthropic、未声明 local 的 exec）只能收到 public 内容——在代码里强制：请求被拒绝、计数，审计里生成 `docs.semantic.classification_refused` finding，impact 里在 judgement.error 里说明。agentic 的工具结果同样过分级（私有路径的 read/grep/list 结果不返回）。
6. **温度**：负责人要求 temperature 0；但当前强模型（Opus 5.5 / Sonnet 5.5）**已不接受采样参数**（发送即 400）。可复现性改由固定提示词 + 结构化回答 + 引用核验 + 冻结评测集保证；ollama 仍用 temperature 0 + 固定 seed。
7. **默认模型**：实例配置示例用 `claude-opus-5-5`（claude-api 参考的当前默认模型；判定需要读懂叙述与代码的关系，且"不为省钱降级"由负责人决定）。Sonnet 5.5 价格一半，是否换由评测数据决定（§9.3 的估算两者并列）。
8. **agentic 模式（v0.4 已实现，见 §3.8）**：试点显示"固定小片段 + yes/no"会饿死强模型（漏判全是"证据不在片段里"）。judge/v1 1.1 预留了 `mode: agentic`，1.2 加上结构化任务（`repo_drift` / `claim_facts` / `doc_facts`）与回答字段 `answer` / `answer_parse`。provider 通过 **judge-tools/v1 1.1**（`list_dir`/`read_file`/`grep`/`search`/`glob`/`git_log`/`git_show`/`git_changes`/`changes_since`/`find_references`/`check_quote`）探索仓库，工具**由核心执行**（provider 永远碰不到文件系统）：`os.Root` 限定在被检 worktree、拒绝绝对路径/`..`/任何名为 `.git` 的路径段（`.github` 照常可读）/符号链接越界、单结果 32KiB、步数与总量上限、未知参数报错、修订号校验防选项注入、私有路径对云端拒绝。传输：exec 协议里 provider 输出 `tool_call` 行、读 `tool_result` 行；claude-code 不给任何内置工具，唯一的工具是 docsp 子进程（`docsp judge-tools-mcp`）经 MCP stdio 提供的同一套只读工具；ollama 用原生 tool calling，核心在进程内执行。snippet 模式的提示词不再调优。

### 3.6 层④c 语义审计（已实现：`docsp audit`）

v0.2 时的约束是"只用本地弱模型"；v0.3 起按决定 A 改为"强模型优先、随时可换"，并由数据分级保证私有内容不出机器（§3.5）。不变的原则：**确定性部分干重活，模型只回答小而可验证的问题**；用强模型对照"强 AI 产出的 GT"得到的只是一致率，不是准确率（§3.7）。

流水线（`internal/semantic`）：

1. **断言抽取**（确定性，`semantic/claim`，fuzz）：从 Markdown 和代码注释里切句（表格行算一句），只保留带可检查内容的句子：带单位的数（时长/大小/百分比/个数）、调度（cron、"daily at 09:00 JST"、"每 5 分钟"）、版本、名字的枚举（代码段并列、`a|b|c`、`a/b/c/d`、以名字开头的列表项）、状态词（planned / not yet / in progress / TODO / 实现中 / 还没有 / 以后 / `状态:` 行）、文件角色（"人写 X" / "generated"），以及句中的标识符。
2. **证据检索**（确定性，`semantic/evidence`）：层③ 符号表；配置值索引（YAML 全部标量、HCL、Go 常量与 flag 默认值、shell/Make 默认值、Dockerfile 镜像、chart 版本）；集合索引（目录的子项、YAML 同级键、同一个键的不同取值、程序的子命令、make 目标、按文档所在目录算的"生成物路径"）；生成标记；按 IDF 的关键词检索（无标识符的句子靠它找文件）。给模型的片段 ≤3 段、每段 ≤2.4KB。
3. **确定性语义检查**（模型之前）：
   - value：`value_mismatch`（只认句中代码段点名的键，单位归一后比较；同名键散在 3 个以上文件且无一相等 → 交给模型）、`schedule_mismatch` / `schedule_disabled`（时区换算；cron 被注释掉 = 停用）、`version_unreleased`（HEAD 可达的 tag 之间不存在的版本；之前/之后的版本不报）、`version_mismatch`、`list_incomplete`（列表 ≥60% 是某集合的成员且覆盖集合 ≥50% 才比较；带"例如/等/such as"的列表不比）。
   - state：`state_contradiction`（句子说"还没有/计划"，而句中标识符**在所有定义处**都是这句话写下之后才加进代码的）、`status_stale`（状态行之后，文档引用的代码有 N 次提交且有新文件，或状态行与实现同一个提交合入）、`role_contradiction`（"人写 X" 而 X 下 ≥80% 文件带生成标记）。
   - staleness：`stale_claim`（句子最后一次修改之后，它点名的代码又被改了 ≥N 次；`git log -L` / `git log A..HEAD`，经白名单 git 包装器）。弱信号，只给 low。
4. **模型**（实例配置的 judge，经 §3.5 的 Guard，可选）：只问一个问题"证据是否直接与断言矛盾？yes/no/unknown"（judge/v1 `claim_contradiction`），结构化输出，必须附上从证据里**逐字复制**的一行；引用核验不过 → 丢弃。自洽：最多 3 次、片段顺序不同，2 票 yes 才算（全票 → medium，2/3 → low），其余为 unknown，不报。私有内容 + 云端 provider → 不发送，报 `classification_refused`。
5. **findings**：collector `docs-audit`，check `docs.semantic.<rule>`，`attributes.docs.detector_layer = semantic`，带 rule / sub_layer / claim / quote（模型：model + prompt_hash + votes）和证据位置；`needs_human = true`，severity 最高 low。

问题优先级（预算有限时先问哪些）：陈旧且带状态词或数值的句子 → 状态词 → 其他陈旧句子 → 数值与列表。

### 3.7 模型评测（已实现：`docsp eval-model`）

- **冻结评测集**：`bench replay -eval-out` 从历史一次性生成（JSON Lines，sha256 记进每次结果），之后不改。来源两类：
  - 正例（期望 yes）：147 条 GT 中能定位到文档行与证据的 129 条，断言 = 固定版本上的那一行，证据 = GT 的 evidence_paths 在固定版本上的片段（按断言用词取窗口，≤3 段 × 2.4KB）。**标签 = "与强 AI 审计一致"，不是准确率**：GT 是强 AI 做的，强模型在它上面得分会虚高，报告里每次都写明。
  - 反例（期望 no）：同一行，对照**写下这一行的那次提交**时的同一批证据文件（假设作者写时与代码一致）——94 条（证据文件当时不存在的跳过）。
  - draft 任务：129 条正例的断言 + 证据，参考答案 = GT 的 fix + actual（只用于打分，从不发给模型）。
- **打分（按任务分开）**：judge → precision、recall、unknown 率、引用拒绝率、按 dev/test 拆分；draft → "无捏造率"（草稿里每个标识符/路径/数字都必须出现在断言或证据里）+ 与参考修复的词重叠（廉价的采纳代理）；两者都有 token、成本、每条成本、p50 延迟。结果写成 eval/v1 JSON，按时间比较。
- **切换规则**：候选 provider 在冻结集上满足实例配置里该任务的全部阈值（`eval.judge`：min_precision / min_recall / max_unknown_rate / max_quote_rejection_rate / max_usd_per_item / max_p50_latency_ms；`eval.draft`：min_supported_rate / min_similarity / 成本 / 延迟）且没有未答项，才可替换当前 provider；不满足退出码 1。生产切换只改实例配置；`--model/--provider` 只用于评测候选。
- `--dry-run` 不调用任何模型，按提示词长度 + provider 固定开销 + 输出假设（judge 700、draft 1000 token，含自适应思考）估算调用数、token 与成本。

### 3.8 层④c 的 agentic 模式（v0.4 已实现：`-mode agentic`）

**为什么**：同一个 Sonnet，在 held-out TEST 集（30 正例 + 30 对照）上，逐行 snippet 设计召回 27%（8/30）；重新设计的原型（在 DEV 上调、TEST 只跑一次）召回 80%（24/30），对照组经复核只有 1 条误报，成本约每行 $0.039 + 每仓库漂移扫描 $0.15–0.25。v0.4 把这个原型移植进产品，经同一套 provider 与 judge 契约，模型仍然只靠实例配置切换。

**两步**（`internal/agentic`）：

1. **仓库漂移扫描**（每个仓库每个 HEAD 一次）：模型读历史（`git_log` / `git_show` / `git_changes`），列出"让旧文档变错的变化"（改名、搬家、删除、换主机、计划变已实现、键/默认值/版本变化……）。**核心逐条机器校验**：提交必须存在且是 HEAD 的祖先（`merge-base --is-ancestor`）、引用必须在 path:line±2 逐字出现、说"已删除/改名/搬走"的旧路径必须真的不存在（否则整条拒绝）；保留条件 = 有一条核验过的引用，或（有效提交 + 旧路径确实不存在）。只有通过的条目（D1、D2…）注入每行判定。结果按 HEAD SHA + 提示词 + 模型缓存（0600，放在用户缓存目录，不进被检仓库）；每次读缓存都重新校验。
2. **逐行判定**（默认）或按文档成组（≤6 行，`-group doc`，更便宜、评测较少）：模型把一行拆成事实，每条给状态 `contradicted` / `incomplete_list` / `outdated_wording` / `dangling_reference` / `unverifiable_in_repo` / `confirmed` 和证据。提示词规则（来自原型）：**别的文档不是证据**；"同名但换了主机、改了名、搬了家、进入了新状态"都算不一致，不要替它开脱。

**四种证据，全部由核心复核**：

| 种类 | 模型给出 | 核心怎么核 |
|---|---|---|
| quote | path、line、逐字文本 | 空白归一后在 path:line±2 出现；≥6 字符；不能是被测行本身（±3）；云端不能引用私有路径 |
| absence | search_pattern、search_scope、search_regex | 用与 `search` 工具**完全相同**的搜索重跑（不区分大小写的定串，或 RE2），范围内所有被跟踪文件（含私有文件，结果不外发），排除被测文档；无命中才接受。范围一个文件都没选中时拒绝（防止拼错目录），除非范围本身就是要找的那个缺失文件（`**/ENVIRONMENTS.md`） |
| drift | drift_id | 必须是本次注入的已校验条目 |
| path_missing（新增） | path | 从根、相对于文档所在目录都不存在；裸文件名时仓库里任何同名文件/目录都不能有；拒绝 glob、URL、越界路径。原型没有这一种，因此漏了一个"引用的 ENVIRONMENTS.md 不存在"的正例 |

**判定由核心推导**：某事实至少一条证据通过即有效（`unverifiable_in_repo` 不需要证据）；按 contradicted > dangling_reference > incomplete_list > outdated_wording 的顺序，第一个被有效事实持有的问题状态即结论；否则全部有效且 confirmed → confirmed；全部有效且只有 confirmed/unverifiable → `unverifiable_in_repo`（单独报告，不算错误、不算 unknown）；其余 unknown。映射到 judge/v1：问题 = yes，confirmed = no，其余 unknown。审计里问题行成为 `docs.semantic.agentic_<category>`（medium，提议）。

**结构化输出**：claude-code 用 `--json-schema` 读 `structured_output`；Ollama 探索结束后再发一次不带工具、`format` = 回答 schema 的请求；两者都有宽容解析器兜底（代码块、前后文字、多余右括号修复），解析失败单独计数（Guard 的 `parse_failures` / `parse_fallbacks`，评测结果里同名分数）。

**可换模型**：一切经 judge/v1 与 Guard；数据分级照旧（云端 provider 收不到私有文档，工具对私有路径拒绝，漂移缓存按模型分开）。以后换弱本地模型只改实例配置（`provider: ollama`，`ai.judge.agentic` 调步数），并用同一冻结集 `eval-model -mode agentic` 比较。

**Provider**：claude-code（MCP 工具服务）、anthropic（Messages API 工具循环，工具调用经 exec 通道由核心执行）、ollama（原生工具调用）、exec。

**与原型的对等验证**：用 `docsp-judge-replay` 把原型冻结的 60 条回答与 3 份漂移列表喂进产品（不调用模型），产品推导出的 60 条结论与"通过核验的证据条数"与原型逐条一致；漂移条目 18/18、15/15 通过。

## 4. 架构

```
                 ┌──────────────────────── docs-platform（产品仓库，通用） ────────────────────────┐
                 │                                                                              │
  repo(只读) ──▶ │  repo（walk / os.Root 读 / git 白名单）                                         │
                 │      │                                                                       │
                 │      ▼                                                                       │
                 │  scan ──▶ Extractor 插件（内置: markdown comments go python shell yaml          │
                 │      │                  make terraform javascript；外部: 任意语言进程）         │
                 │      ▼                                                                       │
                 │  match（层③）  impact（层④a）  residue（层③ 补充）  ai.Provider（④b/c，默认 none）│
                 │      │                                                                       │
                 │      ▼                                                                       │
                 │  findings/v1 文档  impact/v1 文档  extract/v1 文档                              │
                 └──────┬───────────────────────────────────────────────────────────────────────┘
                        │ 只经契约
          ┌─────────────┼──────────────────────────────┐
          ▼             ▼                              ▼
   ledger（与 security-platform 共用）  scorecard / 看板 / 告警     代码托管适配器（PR 评论、草稿 PR）
```

### 4.1 核心概念

| 概念 | 定义 | 代码 |
|---|---|---|
| Symbol | 代码定义的东西（flag、命令、环境变量、指标、配置键、workflow 输入、文件、锚点），带 program 作用域、public、置信度 | `internal/model` |
| Reference | 文档提到的东西，带位置、上下文（code_span / code_block / link / prose / comment）、qualifier（左侧邻词） | `internal/model` |
| Mention | 文档里出现过的 token（反向检查用） | `internal/model` |
| Issue → Finding | 聚合后的问题（同一文档同一对象只报一次，带全部位置）→ findings/v1 | `internal/match`、`internal/findings` |
| Coverage | 本次检查覆盖了什么（`doc:<repo>`、`code:<repo>`），解析失败的文件 = gap | `internal/findings` |

### 4.2 插件接口

```go
type Extractor interface {
    Info() Info                    // Name, Version, Side (docs | code | comments)
    Accepts(path string) bool
    Extract(f File) (model.Result, error)   // 纯函数：只看给定内容，无 I/O，可并发
}
```

- 抽取器 panic 或报错 → 该文件记为 coverage gap，不影响其他文件（`scan.safeExtract`）。台账因此不会把 gap 里的 finding 误判为已修复。
- 外部抽取器：`.docsp.yaml` 中声明绝对路径 argv；docsp 以仓库根为最后一个参数运行（无 shell、最小环境、5 分钟超时、输出上限 64 MiB），stdout 必须通过 extract/v1 校验。这样 Rust/Java/Kotlin 等语言的抽取器可以独立开发、独立发版。
- 可选接口 `plugin.Stripper`：注释抽取器顺便提供"去注释"，核心用它建代码 token 索引。
- 将来的插件点：文档格式（AsciiDoc、reST 只需把文本交给 `refclass`）、代码托管（PR 评论/草稿 PR）、AI provider、层② 执行器、层① 生成器。

### 4.3 CLI `docsp`

| 命令 | 作用 | 退出码 |
|---|---|---|
| `docsp check` | 层③ → findings/v1 + 摘要 | 0；`--fail-on` 命中 → 1；用法/运行错误 → 2 |
| `docsp impact` | 层④a → impact/v1（规则闸门 + 推导的重读段落 + 可选判定/草稿） | 通过/豁免/不适用 → 0；缺文档 → 1 |
| `docsp eval-model` | 按任务在冻结集上评测 provider → eval/v1；`--dry-run` 估算；`-mode agentic -worktree NAME=DIR` 评测 agentic 模式 | 达到切换阈值/估算 → 0；未达到 → 1；错误 → 2 |
| `docsp import-findings` | 已审阅的积压（GT 格式 JSONL）→ findings/v1（collector `docs-backlog`，id 稳定、coverage 为空所以导入永不"修复"任何 finding） | 0 |
| `docsp audit` | 层④c → findings/v1（提议）；`-mode agentic` 用 §3.8 的漂移扫描 + 逐行事实核对 | 0（不因 finding 失败）；用法/运行错误 → 2 |
| `docsp judge-tools-mcp` | （内部）为 claude-code 的 agentic 请求经 MCP stdio 提供只读工具；规格文件由核心写入（0600） | 0 / 2 |
| `docsp extract` | 输出 extract/v1（调试、写插件） | 0 |
| `docsp validate` | 校验 findings 文档 | 0 / 1 |

### 4.4 CI 集成

- **GitHub Action**（仓库根的 `action.yml`，composite）：在 runner 上用 setup-go 从源码构建 docsp，跑 check（产出 findings 文件）和 impact（仅 pull_request）。PR 描述只经环境变量写入文件再交给 docsp，**不进入 shell 脚本文本**，防注入。所有第三方 action 用 commit SHA 固定。
- **普通 CLI**：任何 CI（GitLab、Gitea Actions、Jenkins、本地 pre-push hook）直接跑 `docsp check -fail-on medium` 和 `docsp impact -base <目标分支>`。
- 产品自身的 CI：`.github/workflows/ci.yml` = race 测试、fuzz 冒烟、govulncheck、golangci-lint、以及**自举**（docs-platform 用自己检查自己的文档，`make check-self`）。

## 5. 契约与兼容性

- 四份契约都在 `contracts/`，JSON Schema，文档级 `schema_version`，minor 只加可选字段，消费方忽略未知字段，破坏性变更两步走（与 security-platform、slo-platform 相同）。
- findings/v1：**与 security-platform 共用**。docs-platform 产出的文档在不修改上游 ledger 的前提下已经可以被读取（attributes 本就是自由对象，ledger 原样保存），但严格校验会因两处差异失败——这是本轮要向 security-platform 提的**加法扩展**（§5.1）。契约测试 `internal/check` 的 `TestContract` 证明：除这两处外，输出完全符合上游 1.2 schema。上游 Python 校验器（`core/validate.py`）实测同样只报这两类差异。
- id 规则沿用上游：`sha1(layer|asset|check)[:12]`。asset 不含行号：
  - 正向问题：`doc:<repo>/<文档路径>#<kind>:[<program> ]<subject>`
  - 反向问题：`code:<repo>/<kind>:[<program> ]<subject>`
  - coverage：`doc:<repo>`、`code:<repo>` 为 ok（上游 ledger 的前缀覆盖规则在 `/` 边界生效）；解析失败的文件为 gap。

### 5.1 向 security-platform findings 契约提议的加法扩展（1.3）

| # | 变更 | 类型 | 理由 | 兼容性 |
|---|---|---|---|---|
| 1 | `findings[].layer` 枚举增加 `docs` | 枚举加值 | 文档漂移不属于任何零信任层；硬塞进 `supply-chain` 会污染安全看板 | 旧 ledger 只是原样存储；旧严格校验器需同时升级（两步走：先让 core 接受，再让 docs-platform 产出——docs-platform 已产出，所以 **core 应先合并本扩展再接入**） |
| 2 | `metrics[].name` 模式从 `^security_` 放宽为 `^(security\|docs)_`（或改为按产品登记的前缀表） | 模式放宽 | 指标名属于契约；docs 指标需要自己的命名空间 | 纯放宽 |
| 3 | 正式定义 `attributes.docs`（`$defs.docsAttributes`）：`finding_type`、`ref_kind`、`subject`、`program`、`confidence`、`detector_layer`、`locations[]{path,line,role}`、`suggestion` | 文档化已有的自由字段 | 看板/台账可以按"文档位置 + 代码证据"展示；AI 审计 finding 必须带证据 | 零影响（attributes 本就是自由对象） |
| 4（建议，后续） | 把 `locations[]` 提升为 finding 的可选顶层字段 | 加可选字段 | 安全侧（代码扫描、IaC 检查）同样需要"文件:行"，比各自塞进 attributes 好 | 加法 |
| 5（建议，后续） | 资产类型登记表增加 `doc`、`code` | 文档 | 资产命名约定公开 | 零影响 |

完整的字段映射与 OCSF 对应见 [contracts.md](contracts.md)。

## 6. 台账（ledger）与生命周期

直接复用 security-platform 的 ledger（不另造）：

- open → fixed：只有当本次检查**覆盖了**该文档（coverage ok）而 finding 消失时才判 fixed；文件解析失败（gap）则保持原状态，不会"假修复"。
- 重新出现 = 回归（reopen），计入回归指标。
- 人工：open → accepted（例外，带审批人、理由、到期日）/ false_positive。AI 永远不能做这两个转换。
- `needs_human`：置信度低于 high 的 finding 为 true，提示需要人判断。
- owner：`.docsp.yaml` 的 `owner`；后续可接 security-platform 的 remediation owner 规则（按路径前缀分配）。

## 7. SLO 与指标

| SLI | 定义 | 初始目标（建议） | 数据来源 |
|---|---|---|---|
| 漂移修复时效 | medium 以上 `wrong_or_obsolete` finding 从 first_seen 到 fixed 的天数 | 95% ≤ 14 天 | ledger |
| 文档 PR 覆盖率 | 影响文档的 PR 中带文档或带理由豁免的比例 | ≥ 95%（滚动 28 天） | `docs_impact_changes` |
| 引用解析率 | `docs_reference_resolution_ratio` | ≥ 0.98 | findings 指标 |
| 公开符号覆盖率 | `docs_public_symbol_coverage_ratio` | 先测基线，再定目标 | findings 指标 |
| 检查器新鲜度 | 每个仓库最近一次成功检查距今 | ≤ 1 天 | ledger/collector 心跳 |
| 回归率 | 已修复 finding 重新出现 | 0 / 月为目标 | ledger reopen |

- "没有 finding ≠ 文档正确"：检查器坏了也会没有 finding，所以新鲜度和 coverage gap 本身是 SLI（与 security-platform 的设计一致）。
- 告警按症状：修复时效 SLO 的错误预算燃烧率告警；不对单条 finding 告警。

## 8. 安全与威胁模型（平台自身）

| 威胁 | 对策 |
|---|---|
| 被检仓库内容恶意（路径穿越、超大文件、YAML 炸弹、构造的 Markdown） | `os.Root` 读文件；单文件 2 MiB 上限；YAML 不展开别名、深度/节点数上限；所有解析器 fuzz；抽取器 panic 隔离为 gap |
| 命令注入 | 不用 shell；git 只经 argv 调用且子命令白名单（diff/log/rev-parse/ls-files/show/blame/merge-base）；ref 正则校验且禁止 `-` 开头和 `..`；golangci 的 forbidigo 规则禁止 `exec.Command("sh"/"bash")` |
| PR 描述注入（Action） | 经环境变量 → 文件，不拼进脚本 |
| 外部抽取器 | 绝对路径、无 shell、最小环境、超时、输出上限、契约校验 |
| 秘密泄露 | 秘密/私密文件只读 0600（`internal/secret`）；错误信息不含内容；输出文件 0600 |
| 提示注入（④b/④c） | 仓库内容是数据不是指令；AI 只产出提议；提议再过层③；状态变更只有人能做 |
| 私有内容外发到云端模型 | 实例配置分级（默认 private）；Guard 在发送前拒绝含 private 路径的请求并报 finding；agentic 工具结果同样过滤；exec provider 默认视为云端 |
| 模型凭据泄露 | Anthropic key 只从 0600 文件（否则拒绝）或点名的环境变量读取，不写日志、不回显、错误信息不含 key；claude-code 只用 CLI 自身登录，docsp 不读凭据，CLI 的 stderr 与原始输出不落日志；子进程只拿到 PATH/HOME/USER/TMPDIR 与点名透传的变量 |
| 模型端点被改向 | Anthropic 客户端的 transport 只放行 `https://api.anthropic.com`，不跟随重定向；ollama 只能拨本地地址 |
| agentic 模型越界读取 | 工具由核心执行，`os.Root` 限定 worktree，拒绝绝对路径/`..`/任何 `.git` 路径段/符号链接越界，大小与步数上限，git 只允许只读子命令（log/show/blame/merge-base --is-ancestor）且修订号校验；claude-code 没有任何内置工具（`--tools ""`），只有 docsp 提供的 MCP 只读工具，工作目录是空临时目录 |
| agentic 模型编造证据 | 证据全部由核心复核（引用逐字、搜索重跑、漂移条目先校验、路径真实不存在），未通过的证据作废；判定只从通过的证据推导 |
| 成本失控 | 每任务每次运行的硬预算（请求数/token/USD），发送前按最坏情况预判；dry-run 先估算 |
| findings 本身敏感（私有仓库结构、denylist 命中） | findings 视为私有数据，存放在实例的私有数据仓库，不进产品仓库 |

## 9. 基准与当前效果（2026-11-25）

`bench/`：从公开仓库只读克隆到草稿目录，进程内跑 check，对照人工 ground truth（JSON Lines），按 finding 类型、ground truth 类型、估计层（reference / semantic）、置信度阈值报告。ground truth 不穷尽，所以 precision 是**下界**；未匹配的 finding 全部列出供人工分诊。

数据：3 个仓库共 147 条人工确认问题（wrong 30 / obsolete 62 / missing 55）。层归属用启发式估计：reference 23 条（有明确标识符或"不存在"），semantic 124 条。

结果（2026-11-25，带实例配置 `related_repos`；api-factory@d7555c9、gitops-manifests@9218a24、infrastructure-live@25db814）：

| 最低置信度 | findings | precision 下界 | 召回（全部） | 召回（reference 层） | 召回（semantic 层） |
|---|---|---|---|---|---|
| high | 109 | 9% | 4/147（3%） | 3/23（13%） | 1/124 |
| medium | 214 | 9% | 14/147（10%） | 7/23（30%） | 7/124 |
| low | 345 | 8% | 21/147（14%） | 10/23（43%） | 11/124 |

解读：

- **precision 下界被 `undocumented` 拉低**：medium 阈值下 200/214 条是"公开符号没有文档"（多数是 CI 内部脚本的环境变量与 flag）。这些按定义都是真的（文档里确实一次都没出现），但人工审阅者不认为重要，所以不在 ground truth 里。它衡量的是"相关性"而不是"正确性"。
- **`wrong_or_obsolete`（medium 以上）14 条**：5 条与 ground truth 匹配；其余人工分诊：3 条是 ground truth 未收录的真实漂移（注释里的 `int32.go`、`date.go` 早已不存在；一处指向已不存在的 schema 文件），4 条是"没注明出处的跨仓库路径"（读者在本仓库里找不到，属文档缺陷但可商榷），1 条误报（另一仓库的环境变量），1 条不确定（设计文档里的计划目录）。即人工分诊后的 precision 约 57%–86%。
- **reference 层召回**：漏掉的主要是"应写在**某个特定文件**里"的遗漏（指标、环境变量在别的文档里提过，所以反向检查认为已文档化）——这需要"文档段落 ↔ 代码模块"的映射，属于 ④a 的映射或 ④c。
- **语义层几乎不可达**：124 条里能碰上的个位数是偶然（同一行恰好有一个可判定的错误），不应计为能力——这正是 ④b/④c 的工作量证明。
- 不带实例配置时 medium 阈值多 4 条跨仓库路径误报，召回不变。
- 结论：层③ 作为 CI 闸门只启用 `wrong_or_obsolete` 的 medium 以上；`undocumented` 先作为台账里的 info/low 观察指标，等每个仓库配好 `reverse.kinds` 与 ignore 再升级为闸门。

### 9.1 层④c 语义审计（2026-11-25）

纪律：api-factory 是开发集（设计时只看它的 ground truth），gitops-manifests + infrastructure-live 是留出的测试集（规则冻结后才跑、才看）；没有逐条规则、没有仓库特判。语义 finding 与 ground truth 的匹配 = 同一文件、行号 ±1。模型 = 本地 gemma4:12b（Ollama，think 关，temperature 0，固定 seed，并发 2，每仓库最多 150 个问题）。

测试集（73 条，主结论）：

| 组 | findings | precision 下界 | recall | wrong | obsolete | missing |
|---|---|---|---|---|---|---|
| value（值/调度/版本/列表） | 8 | 2/8 | 3/73 | 1/13 | 0/34 | 2/26 |
| state（状态词/状态行/角色） | 0 | – | 0/73 | | | |
| staleness（`stale_claim`，low） | 35 | 7/35 | 8/73 | 3/13 | 2/34 | 3/26 |
| model（ollama） | 8 | 0/8 | 0/73 | | | |
| 语义合计 none / ollama | 43 / 51 | 9/43 / 9/51 | 11/73（15%） | | | |
| 层③ 单独 → 层③ + 语义（low） | 178 → 221 | | 9/73（12%）→ 19/73（26%） | | | |

开发集（74 条）：value 3/12（recall 3）、state 3/4（recall 3：两条"状态: 实现中"、一条"还不是内"）、staleness 11/170、model 1/5；语义合计 recall 18/74（24%），medium 以上 4/7 命中；层③ + 语义（low）recall 16% → 39%。

成本：每仓库 148–149 个问题 ≈ 300 次请求、约 5–5.5 分钟（≈1 req/s；真实提示词以 prefill 为主，并发 1 → 0.92 req/s，并发 2 → 0.96 req/s，显存恒定 8.86 GB）；无模型时整个审计 1–16 秒。引用核验丢弃：开发 5、测试 18 次 yes 回答（引用不在证据里、或只是抄了断言、或与断言无共同词）。

解读：

- 确定性部分可靠但覆盖窄：命中的都是"可算的"——不存在的发布版本、按提交历史已过时的状态行、"还没有"的东西其实已在之后加入、枚举少了成员。测试集上 ground truth 以叙述性"过时/遗漏"为主（obsolete 34 条），确定性规则几乎够不到。
- 弱模型保守：绝大多数回答是 no；人工复核模型的 13 条 yes（开发 5 + 测试 8），真实漂移约 3 条（itest.go 注释漏了 `RUNTIME_HELPER*`、quiet-traffic-day 时长 4.5h→2h、adding-a-primitive 闸门列表）、2 条可商榷、其余为误判。模型层在测试集上没有命中 ground truth：**弱本地模型 + 引用核验能压住幻觉，但不能替代对叙述的理解**。
- staleness 是最大的召回来源，但精度只有 6–20%，只能当"该重读这句了"的低优先级信号。
- 已知误报（测试集发现，未改以免对答案调参）：`version_mismatch` 把 Helm chart 版本（9.2.2）与注释里的应用版本（Falco 0.45.2）相比；`list_incomplete` 的"同一键的取值"集合跨文件混用（`owner`）。

Ground truth 质量抽查（15 条分歧：10 条工具漏掉的 GT + 5 条 GT 未收录的工具 finding，逐条对照代码）：GT 明显错误 1 条（`loadtest/README.md:21` 的项目名在固定版本 25db814 已被删除），可商榷 3 条（hardening.md:153 与 bottlenecks.md:15 是历史记录而非现状陈述；infrastructure-live README.md:39 的"遗漏"属主观完整性），其余 6 条 GT 正确；5 条工具 finding 中 2 条是 GT 漏收的真实漂移（itest.go 注释、storefront 示例 README 的生成物列表漏了 `Dockerfile`），3 条是工具误报。

### 9.2 历史回放：变更发生时有没有标出来（2026-11-25，主指标，与模型无关）

方法（`go run ./bench replay`，provider none，纯确定性）：对每条 GT，`git blame` 找到最后写下那一行文档的提交 Cd；Cd 之后（first-parent）修改过该条 evidence_paths（不含文档本身）的提交 = **致因提交**。每个致因提交都按它自己的 diff、在它自己的树上（`git archive` 物化到临时目录）跑与 `docsp impact` 相同的推导；那一行所在的段落以 `reread` 出现在列表里 = 命中（这次变更自己改过的 `updated` 段不算）。首个致因提交 = "变旧的那一刻"的最佳估计；"任一"给出上界。开发集 api-factory，测试集 gitops-manifests + infrastructure-live（推导规则在看测试集之前已定，未按测试集调过）。

| 切片 | GT 条数 | 可评分 | 无致因提交 | 首个致因提交即标出 | 任一致因提交标出 | 命中经路径 / 标识符 |
|---|---|---|---|---|---|---|
| dev（api-factory） | 74 | 60 | 14 | 21/60（35%） | 32/60（53%） | 13 / 19 |
| **test（gitops + infra，留出）** | 73 | 61 | 12 | **32/61（52%）** | **41/61（67%）** | 29 / 12 |
| test / wrong | 13 | 10 | 3 | 6/10 | 8/10 | |
| test / obsolete | 34 | 30 | 4 | 10/30 | 14/30 | |
| test / missing | 26 | 21 | 5 | 16/21 | 19/21 | |

代价（每次被回放的变更要重读多少段）：dev 81 个提交，平均 36.9 段（中位 18，p90 96），占全部段落的 2.0%（树里平均 2549 段）；test 134 个提交，平均 11.0 段（中位 8，p90 30），占 5.3%（平均 257 段）。随机挑同样多的段落，命中概率 ≈ 2–5%，所以 35–52% 的首提交命中率是实打实的信号。

无致因提交的 26 条单独列出（报告里逐条写原因）：多数是 evidence_paths 只有文档本身（注释类 GT，代码与注释在同一文件）、或证据文件在文档那一行写下之后再没变过（"写下时就是错的"或证据在别处）；1 条 GT 的行号超出文件（`.gitignore:46`，文件只有 40 行）。

解读：

- **变更时检测是可行的**：测试集上一半以上的漂移在"让它变旧的那次提交"就能把正确的段落列出来，不需要模型、不需要手写映射。路径提及是测试集的主要来源（29/41），标识符是开发集的主要来源（19/32）。
- 召回漏掉的主要是：段落没有提到被改的文件或标识符（叙述性描述、README 第 1 行这种整篇性的"遗漏"）；以及 obsolete 类（测试集 14/30）——文档描述的是"一组东西"，变更新增了一个成员但没出现旧名字。
- **噪声是 api-factory 的问题**：大仓库里一次变更平均要重读 37 段，p90 近百段，直接做 PR 评论会被忽略。下一步（只在开发集上调）：term 类标识符按 IDF 降权、同一段多理由排序、每次只展示前 N 段。
- 推导结果目前只作建议；升级为闸门之前，先要在负责人的 PR 上跑 ≥2 周看采纳率。

### 9.3 模型评测就绪度与成本（2026-11-25）

- 冻结集：`<草稿目录>/replay/evalset.jsonl`，judge 223 条（正 129：dev 68 / test 61；反 94：dev 43 / test 51），draft 129 条。
- 端到端冒烟（claude-code，`claude-opus-5-5`，公开仓库内容，共 4 次真实调用）：1 正 + 1 反判定正确（yes 的引用通过核验），1 条草稿无捏造、与参考修复词重叠 0.33；每次判定实测约 2.1k 输入 / 0.56k 输出 token、6–9.5 秒。
- 全量估算（`--dry-run`，不调用模型；claude-code 的固定开销按实测 ~1.3k token/次计入；输出假设 judge 700、draft 1000 token）：

| 任务 | 调用数 | 输入 token | 输出 token | Opus 5.5 标价 | Sonnet 5.5 标价 |
|---|---|---|---|---|---|
| judge | 223 | 543k | 156k | $5.30 | $2.65 |
| draft | 129 | 310k | 129k | $3.82 | $1.91 |
| 合计 | 352 | 853k | 285k | **$9.11** | **$4.56** |

  claude-code 走订阅：不产生 API 账单，但消耗订阅额度（上表为标价折算的名义成本）；anthropic provider 走 API 时少了 CLI 开销，Opus 约 $7.3、Sonnet 约 $3.6。冒烟实测每条判定 $0.025 / 草稿 $0.019，与估算（$0.024 / $0.030）一致或更低。
- **注意**：这个评测集是 snippet 模式的。agentic 模式（§3.8）用自己的冻结集：断言 + worktree 修订号，不带片段（`task_type: claim_facts`），见 §9.4。

### 9.4 agentic 模式（2026-11-26）

- 冻结集：原型的 TEST 集（30 正例 + 30 经复核为真的对照，2 个仓库），条目带仓库修订号；`eval-model -mode agentic -worktree NAME=DIR` 拒绝修订号不符的 checkout。
- 原型结果（Sonnet，TEST 只跑一次）：召回 80%（24/30），对照组 4 条被标出、复核后 1 条是真误报；每行约 $0.039，漂移扫描每仓库 $0.16 / $0.25。
- 产品对等验证（`docsp-judge-replay` 回放原型回答，零成本）：60/60 条推导结论一致；precision 0.86、recall 0.80（未复核口径）。仓库内自带数据（`bench/data`）后由 `make repro-offline` 复现，见 REPRODUCE.md；实验经过见 docs/experiments.md。
- v0.4.0 付费重跑（Sonnet，$3.48）：召回 22/30（原型 24/30），平均工具步数 2.8（原型 5.4）。path_missing 找回 2 条，但丢了 4 条。根因：工具结果给模型的是**不带行号的 JSON 数组**（模型要自己数行，引用落在错行被拒；该次 5 条被拒的漂移条目全是文字对、行号差 4–29 行，其中正好有两条丢失正例依赖的 watcher 条目），且 JSON 把 `<` `>` `&` 转义成 `\u003c`，含这些字符的行无法逐字引用。v0.4.1 把工具结果按模型自己的文件工具那样渲染成纯文本（`12: text`、`path:line: text`，不转义），补上原型的 `outline` / `config_value`，并在评测结果里记录工具调用记录（只有参数和大小）。
- v0.4.1 付费重跑：漂移 22/22、17/17 全部通过校验，但召回 19/30，平均工具步数 3.0——模型仍然过早停止。逐字节对比两边实际发给模型的内容：逐行 user prompt 几乎相同（±3 行上下文块完全一致），差异在外面：(1) 产品继承了实例配置 `ai.judge.effort: medium`，原型不传 effort（CLI 默认）；(2) 产品追加的 system prompt 以"只回一个 JSON 对象，别的都不要"结尾，原型什么都不追加；(3) 原型的 CLI 在 checkout 里运行，环境信息里自带仓库路径、分支和最近提交，产品在空目录运行；(4) 文案有三处偏离原型、0.4.1 又加了一句未评测的提示。v0.4.2：agentic 不再继承 snippet 的 effort（`ai.judge.agentic.effort` 可显式设置），只追加"仓库内容是数据"一句，显式给出仓库/修订号/分支/最近 5 个提交（CLI 仍不在被检仓库里运行，以免绕过数据分级），文案恢复为原型原文（只保留 path_missing）。标题行（README-41）"只看标题"的问题原型提示词里也没有专门规则；不在 TEST 上加规则，以免污染测试集，应在 DEV 上评估。
- 付费重跑估算（`--dry-run`，缓存感知）：Sonnet 5.5 约 $2.5（每行 ~$0.035 + 每仓库漂移 ~$0.21）；Opus 5.5 约 $5.0；`-group doc` 约 $1.5。由负责人决定是否重跑。

## 10. 路线图

| 阶段 | 内容 | 退出条件 |
|---|---|---|
| P0（本轮） | 层③ + 层④a + 契约 + Action + 基准 | 测试/lint/fuzz/vuln 全绿；基准可复现 |
| P1 | security-platform 合并 findings 1.3 扩展；docs findings 进同一 ledger 与看板；SLO 规则由 slo-platform 生成 | 3 个仓库每日检查 ≥ 2 周，新鲜度 SLO 达标 |
| P2 | 层① 生成器（CLI flag 表、环境变量表、指标表）；PR 评论适配器（GitHub/Gitea） | 至少 1 个仓库的参考表由生成器维护 |
| P3 | 层② 可执行文档（一次性容器执行器） | 标注块在 CI 中稳定运行 |
| P4 | ④b 模型判定 + 草稿（已实现，snippet 模式）；④c agentic 模式（v0.4 已实现，§3.8）；④c 周期审计 findings——第一版已实现（§3.5–3.8、§9） | 历史回放测试集首提交命中 ≥ 60%（当前 52%）且每次变更重读段数中位 ≤ 10；judge 在冻结集上达到实例配置阈值 |
| P4.5 | 持续一致性（§12）：证据台账与失效、循环盘点调度、一致性 SLO、仓库组、去抖与最小 diff 草稿（backlog：docs/backlog.md） | 台账在真实历史回放上的致因提交命中率高于影响选择（52% / 67%），且每次提交平均 stale 断言数 ≤ 断言总数的 5% |
| P5 | 更多抽取器（Java/Kotlin、Rust、OpenAPI、Helm 模板引用、Ansible tag）；外部抽取器样例仓库 | 每个新抽取器自带 fixture + fuzz |

## 11. 待产品负责人决定的事项

1. **findings 契约扩展的落地顺序**：是否同意先在 security-platform 合并 1.3（layer `docs` + `docs_` 指标前缀 + `attributes.docs`），再让 docs findings 进共用 ledger？还是 docs-platform 先用独立 ledger 实例？
2. **指标前缀**：只加 `docs_`，还是改成"按产品登记的前缀表"（以后 slo-platform 等也能用）？
3. **`undocumented` 的默认策略**：默认全部公开符号都要求文档（现状，信息量大、噪声也大），还是默认只要求 CLI flag/命令 + 环境变量，其余按仓库在 `reverse.kinds` 里打开？
4. **注释算不算文档**：现在注释里的路径/标识符会被检查（基准里 1/3 的问题在注释里），但也引入了更多 low 置信度噪声。是否保持默认开启？
5. **闸门阈值**：CI 默认 `fail-on medium`（只挡高置信度的 wrong/obsolete）是否合适？
6. **残留词 denylist 的存放位置**：放在私有数据仓库（与 findings 同级，0600），由实例配置指向；是否接受？
7. **AI provider**：已按决定 A 改为"强模型优先、可换"（§3.5）。待定：(a) 何时跑全量评测（估算见 §9.3，需负责人放行）；(b) judge 的默认模型 Opus 5.5 还是 Sonnet 5.5——等评测数据；(c) anthropic provider 默认开启 server-side refusal fallback（被拒时由其他模型作答，回答里记录实际模型），是否保留；(d) 每周审计的请求预算（默认 300 次/任务）与 `stale_claim` 是否进台账。
10. **变更时推导何时升级为闸门**：目前只建议；建议在负责人的 PR 上影子运行 ≥2 周、测采纳率后再定。
11. **积压导入**：147 条 GT 导入共用 ledger 之前需要 findings 1.3（第 1 项）落地；在此之前导入结果只存私有数据仓库。
9. **④c 的下一步**：P4 退出条件（语义召回 ≥ 50%）用弱模型 + 当前问题形式达不到。可选：(a) 增加确定性规则覆盖（叙述性"过时"大多是"文档描述的计划已合入"——可以用提交信息/PR 标题与文档标题的对应来判定）；(b) 换问题形式（"证据里有没有断言没提到的成员"之类的补全问题，同样要求逐字引用）；(c) 接受 ④c 只是"低成本的提示器"，召回目标下调。
12. **agentic 模式的下一步**：(a) 是否用产品对 TEST 集付费重跑一次（Sonnet 约 $2.5）以确认移植后的真实召回；(b) 默认分组用 line（已评测）还是 doc（便宜约 40%，未单独评测）；(c) ~~anthropic provider 是否也实现 agentic~~ 已实现（Messages API 工具循环，工具调用经 exec 通道回到核心执行；仅假服务器单测，尚未付费实测）；(d) 弱本地模型的替换评测何时做。
8. **发布**：仓库何时公开、许可证 Apache-2.0 是否最终确定、模块路径 `github.com/MIERAI/docs-platform` 是否沿用。

## 12. 持续一致性：消灭文档的"幽灵库存"（v0.5 设计，负责人已批准）

### 12.0 产品目标

**让文档在时间维度上持续与代码一致，而不是一次性对齐。** 负责人的说法：解决文档的"幽灵库存"——就像零售里系统显示有货、货架上却没有，文档声称存在/成立的东西其实已经不存在或不再成立。借用零售库存管控的三件工具：

| 零售 | 这里 |
|---|---|
| 库存记录准确率（IRA） | 断言准确率：抽样复核里"文档说的仍然成立"的比例（SLI 1） |
| 循环盘点（cycle counting） | 新鲜度策略：每条断言在周期内必被重新核实；变动大的先盘 |
| ABC 分类 | 高变动 / 高重要的文档（A 类）盘得更勤，C 类少盘 |
| 账实不符的"幽灵库存" | 悬空引用（不存在的路径/文件/名字）——确定性可测，目标 ≈0（SLI 3） |

一次性审计（§3.8）回答"现在哪里错了"；本章回答"怎样一直不错，并且花得起"。核心思想：**判定一次，记下判定所依据的全部证据；之后只有证据变了才重判。**

### 12.1 断言与证据台账（evidence ledger）与基于证据的失效

- **断言（claim）**：一条被判定过的文档行。字段：id（仓库 + 文档路径 + 行文本的规范化哈希，与行号无关，行移动不换 id）、repo、文档路径、行号与行文本、文档文件在判定修订号上的 blob、类别（§3.8 的六类 + unknown）、verdict、`last_verified_at`、`last_verified_ref`（repo@commit）、模型、prompt_hash、事实列表（文本、状态、是否有效）。
- **证据（只记核验通过的）**：地址统一为 `repo@ref:path:line`。
  - quote：path、被引行号、该文件在 ref 上的 blob 哈希。
  - drift：漂移条目 id、其提交、其引用文件的 blob、"已删除"旧路径。
  - absence：pattern、glob、regex 标志，以及该 glob 在 ref 上覆盖的文件集合（路径 + blob 的哈希、文件数）。
  - path_missing：目标路径（ref 上确认不存在）。
- **失效（确定性，不调用模型）**：对新修订号 R'，某断言判为 stale 当且仅当：文档文件里该行文本不在了（文档改了）；任一 quote / drift 证据文件的 blob 变了或消失；任一 absence 范围内有文件新增、删除或改变（集合哈希变了）；任一 path_missing 目标出现了；或 drift 条目的旧路径重新出现。只有 stale 断言被重判。
- **没有证据的断言**（unknown、unverifiable_in_repo）无从失效，进入周期复核（§12.2）。
- 来源：公司某流水线用 blob 哈希复核，但只盯入口文件；这里盯的是**全部证据**，包括"没有"（absence）这种负证据的覆盖范围。
- 台账是私有数据：文件 0600、默认放在实例的私有数据目录，从不写入被检仓库；版本化契约 `contracts/ledger-v1.schema.json`。
- 命令：判定（audit / eval-model 的 agentic 模式）写台账；`docsp stale -ledger FILE -ref R' REPO` 输出 stale/v1（每条断言 fresh / stale / recheck 与原因）。

### 12.2 新鲜度策略：循环盘点

- 每条断言有 `last_verified_at`、`last_verified_ref`。满足任一即到期：年龄 > `max_age_days`；自核实以来证据范围内的变动（提交数或变更行数）超过阈值。
- **调度**：每次运行在预算（请求数 / USD）内，按优先级挑断言：stale（证据变了）> 到期 > 无证据周期复核；同优先级内按 ABC 权重和年龄。保证每条断言在一个周期（`max_age_days`）内至少被核实一次：调度器按"剩余天数 × 剩余条数"算出本次最少要盘的数量，预算不够则报告缺口（新鲜度 SLI 下降，而不是悄悄跳过）。
- **ABC**：A = 证据范围高变动（近 90 天提交多）或被标为重要（运维手册、入门文档）；C = 低变动；A 的 `max_age_days` 是 C 的若干分之一（实例配置）。

### 12.3 文档一致性 SLO 与错误预算

- **SLI 1 准确率**（库存记录准确率）：每周随机抽样一批断言，由强模型在 harness 校验下完整重审（不看台账结论）；抽样里"仍成立"的比例。抽样请求只能由运营者显式运行（付费）。
- **SLI 2 新鲜度**：在 `max_age_days` 内核实过的断言比例。
- **SLI 3 幽灵率**：确定性悬空引用（层③ 的未解析路径/文件/名字 + path_missing）占引用的比例，目标 ≈0。
- **初始 SLO**：准确率 ≥ 80%（错误预算 20%），按实测逐步收紧到 90%、95%。
- **预算燃烧**：某文档集的预算烧完 → 该集全量重审，并缩短其 `max_age_days`。
- 报告：三个 SLI、剩余预算、成本（tokens / USD，按订阅还是 API 计费分开）。

### 12.4 仓库组与声明的修订号

- 实例声明一个仓库组，每个仓库有角色：code / docs / api-spec / db-migration / infra。证据地址 `repo@ref:path:line`，失效可以跨仓库（文档在 docs 仓库，证据在 code 仓库）。
- 漂移扫描对每个 code `repo@ref` 只做一次，组内共享。
- 每个文档集声明它描述的修订号（设计文档对 develop、运行手册对 prod，可多个）；判定与失效都对声明的修订号做。
- 生成的文档（生成标记）不在文档上判，改到生成器的输入上判。子模块与外部来源算 unverifiable。

### 12.5 不强制更新

- **不设"代码改了文档没改"的闸门**。只报告经过核验的矛盾。
- **去抖**：对每个文档集声明的修订号判定，批量出报告，而不是每次提交一份。
- **严重度**：contradicted / dangling 立即报告；outdated_wording / incomplete_list 进批次。
- **抑制**必须带理由和到期日；过期自动失效。
- **草稿修复**是最小 diff：只改被否定的事实；优先删除或改成链接，而不是写入新的易变细节。
- 跟踪草稿采纳率、回滚率、diff 大小；某文档反复变旧 → 建议"改为生成"或"改为链接"。

### 12.6 整篇审计（行选择）仍是实验线

整篇文档选哪些行去判（§3.6 的断言抽取、或让模型选行）继续作为实验；它与本章的接口是台账：审计产生断言，断言进台账，之后由失效与循环盘点维持。

### 12.7 离线验证方法与 B1 结果（2026-11-26，零模型调用）

- 台账：用冻结的 TEST 回答回放（零模型调用）在 TEST 修订号上建台账。
- 失效：沿真实历史逐提交回放，统计每次提交使多少断言 stale；对 GT 正例，检查其"致因提交"（§9.2 的定义）是否会触发失效，与变更时影响选择的 52% / 67% 对比。
- **结果**（`bench ledger-replay`；台账 = 冻结 TEST 回答在 TEST 修订号上回放出的 60 条断言）：
  - 成本：gitops-manifests 329 个提交，每提交平均使 0.66 条断言 stale（中位 0、p90 2、最多 11），83% 的提交一条都不 stale，平均占存活断言的 5.0%；infrastructure-live 50 个提交，平均 0.80（p90 3），64% 的提交不 stale，占 12.0%。
  - 召回：同一批 25 条可打分正例上，台账在**第一个致因提交**就判 stale 19/25（76%），**任一致因提交** 23/25（92%）；变更时影响选择在同样 25 条上是 9/25（36%）/ 13/25（52%）。
  - **注意（偏乐观）**：证据足迹取自 TEST 时刻的回答——这些回答引用的正是"揭示它已过时"的文件，而致因提交又按 GT 的证据路径定义，两者部分重合。真实台账在文档写下时建立，足迹是"当时证实它的证据"，未必包含后来变化的文件。真正的检验要在 DEV 上于更早的修订号建台账（付费）或从现在起向前跟踪。

### 12.8 实现状态（未发布，见 CHANGELOG "Unreleased"）

| 项 | 命令 / 契约 | 状态 |
|---|---|---|
| B1 证据台账与失效 | `docsp stale`；ledger/v1、stale/v1；`bench ledger-replay` | 已实现（§12.7 的离线结果） |
| B2 循环盘点与 SLO | `docsp schedule / recheck / sample / slo`；schedule/v1、slo/v1；实例 `consistency:` | 已实现；准确率抽样的付费重审由运营者执行 |
| B3 仓库组与声明修订号 | `docsp group plan / judge / stale`；实例 `groups:`；台账 `target`、`doc_ref` | 已实现；docsp 不改 checkout，目标需在其修订号上有 checkout |
| B4 不强制更新 | `docsp report / draft / drafts`；report/v1；实例 `batch_days`、`suppressions`、`recommend_after` | 已实现 |

