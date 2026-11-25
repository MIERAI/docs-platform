# docs-platform 设计书

状态：v0.2（层③ 引用检查 + 层④a PR 影响分析 + 层④c 语义审计已实现；其余为设计）
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
| 版本化、语言中立的契约 | `contracts/` 下 4 份 JSON Schema：findings/v1（与 security-platform 共用）、extract/v1、impact/v1、config/v1；各部件只经契约耦合 |
| 各部件独立升级 | 每个抽取器有自己的 name/version，写入 findings 的 `collector.tools`；外部抽取器是独立进程，任意语言 |
| 产品仓库不含环境信息 | 仓库里没有任何主机、仓库名、人名；`related_repos`、denylist、基准用的实例配置都在仓库外 |
| "不优秀就活不长" | golangci-lint 严格配置（函数 ≤50 行、认知复杂度、gosec、wrapcheck…）、表驱动测试、每个外部输入解析器都有 fuzz、golden 测试、契约测试、govulncheck、唯一第三方依赖 yaml |
| 平台自身安全 | 对被检仓库只读（`os.Root` 防越界读、git 子命令白名单、无 shell、无执行仓库内容）；私密文件只读 0600；输出文件 0600 |
| AI 只提议 | `ai.Provider` 只返回 Proposal；默认 provider = `none`；状态变更（关闭/接受 finding、合并 PR）只有人能做 |

## 3. 四层检测

| 层 | 检什么 | 判定方式 | 何时跑 | 本轮 |
|---|---|---|---|---|
| ① 生成的参考文档 | CLI 帮助、配置项表、指标表、环境变量表由代码生成，CI 比对 | 确定性（diff） | 每个 PR | 设计 |
| ② 可执行文档 | 文档里的 shell 代码块在 CI 沙箱里真跑 | 确定性（退出码/输出） | 每个 PR / 每夜 | 设计 |
| ③ 引用检查 | 文档提到的标识符必须存在；公开符号必须有文档 | 确定性（符号表对账）+ 置信度 | 每个 PR + 定期 | **已实现** |
| ④ 语义 | (a) PR 影响分析 (b) 合并后 AI 提议文档 PR (c) 定期语义审计 → findings | (a) 确定性映射；(b) AI 提议 + 人审；(c) 确定性语义检查 + 本地弱模型的可验证小问题 + 人审 | PR / 合并后 / 每周 | **(a)(c) 已实现**；(b) 接口 |

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

### 3.4 层④a PR 影响分析（已实现）

- `.docsp.yaml` 的 `impact.rules`：`{name, code: [globs], docs: [globs]}`。
- `docsp impact --base --head`：`git diff --name-only base...head`（合并基语义，等同 PR 视图）；规则命中代码但未命中文档 → `missing_docs` → 失败。
- 豁免：提交信息或 PR 描述里的 trailer `Docs-Not-Needed: <理由>`（键名可配）。**没有理由的 trailer 不算豁免**；豁免理由写进 impact/v1 报告，可审计。
- 指标：每次运行输出 `docs_impact_changes{impacting, with_docs}`，聚合后就是 SLI"影响文档的 PR 中带文档（或带理由豁免）的比例"。
- 设计取舍：映射是**声明式**的而不是"AI 判断这个 PR 要不要改文档"，因为闸门必须可预测、可复现、可被开发者本地预演；AI 的判断放到 ④b 做提议。

### 3.5 层④b 合并后 AI 提议文档 PR（设计）

触发：PR 合并且 impact 结果为 `waived` 或规则未覆盖但 diff 触及 public symbol（extract/v1 前后对比得到"符号增删改"）。

1. 确定性部分先算好上下文：变更的符号（前后两次 extract 的差集）、映射到的文档段落、层③ 在新 head 上的 findings。
2. `ai.Provider.Propose` 只拿到上述**数据**（仓库内容视为不可信数据，不是指令；提示注入在威胁模型内），返回 Proposal{path, unified diff, rationale, evidence}。
3. 由代码托管插件（GitHub/Gitea 适配器）以**草稿 PR**形式提交到独立分支，标注"AI 提议，需人审"。适配器凭据来自 0600 文件或环境变量。
4. 提议本身再跑一遍层③：若引入新的 unresolved 引用，直接丢弃，不打扰人。
5. 永不自动合并；台账里 AI 不能把任何 finding 标为 fixed/accepted。

### 3.6 层④c 语义审计（已实现：`docsp audit`）

产品负责人的硬约束：**只用弱的本地模型**（不用强模型/云端）。理由：基准 ground truth 本身就是强 AI 对照代码读文档产出的，用强 AI 复现它什么也证明不了；产品也不能依赖强 AI；代码和文档不得离开负责人的机器。所以设计原则是**确定性部分干重活，模型只回答小而可验证的问题**。

流水线（`internal/semantic`）：

1. **断言抽取**（确定性，`semantic/claim`，fuzz）：从 Markdown 和代码注释里切句（表格行算一句），只保留带可检查内容的句子：带单位的数（时长/大小/百分比/个数）、调度（cron、"daily at 09:00 JST"、"每 5 分钟"）、版本、名字的枚举（代码段并列、`a|b|c`、`a/b/c/d`、以名字开头的列表项）、状态词（planned / not yet / in progress / TODO / 实现中 / 还没有 / 以后 / `状态:` 行）、文件角色（"人写 X" / "generated"），以及句中的标识符。
2. **证据检索**（确定性，`semantic/evidence`）：层③ 符号表；配置值索引（YAML 全部标量、HCL、Go 常量与 flag 默认值、shell/Make 默认值、Dockerfile 镜像、chart 版本）；集合索引（目录的子项、YAML 同级键、同一个键的不同取值、程序的子命令、make 目标、按文档所在目录算的"生成物路径"）；生成标记；按 IDF 的关键词检索（无标识符的句子靠它找文件）。给模型的片段 ≤3 段、每段 ≤2.4KB。
3. **确定性语义检查**（模型之前）：
   - value：`value_mismatch`（只认句中代码段点名的键，单位归一后比较；同名键散在 3 个以上文件且无一相等 → 交给模型）、`schedule_mismatch` / `schedule_disabled`（时区换算；cron 被注释掉 = 停用）、`version_unreleased`（HEAD 可达的 tag 之间不存在的版本；之前/之后的版本不报）、`version_mismatch`、`list_incomplete`（列表 ≥60% 是某集合的成员且覆盖集合 ≥50% 才比较；带"例如/等/such as"的列表不比）。
   - state：`state_contradiction`（句子说"还没有/计划"，而句中标识符**在所有定义处**都是这句话写下之后才加进代码的）、`status_stale`（状态行之后，文档引用的代码有 N 次提交且有新文件，或状态行与实现同一个提交合入）、`role_contradiction`（"人写 X" 而 X 下 ≥80% 文件带生成标记）。
   - staleness：`stale_claim`（句子最后一次修改之后，它点名的代码又被改了 ≥N 次；`git log -L` / `git log A..HEAD`，经白名单 git 包装器）。弱信号，只给 low。
4. **模型**（`ai.Provider` `ollama`，可选）：只问一个问题"证据是否直接与断言矛盾？yes/no/unknown"，结构化输出（JSON schema），必须附上从证据里**逐字复制**的一行；引用在片段里找不到（或只是抄了断言本身）→ 丢弃。自洽：最多 3 次、片段顺序不同，2 票 yes 才算（全票 → medium，2/3 → low），其余为 unknown，不报。temperature 0、固定 seed、think 关、每次运行请求预算、并发上限（实例配置，默认 2）、重试、超时。HTTP 客户端在拨号时检查解析后的地址，只允许 loopback / 私网 / CGNAT（tailnet）/ link-local——配错端点也不会把仓库内容发到公网。
5. **findings**：collector `docs-audit`，check `docs.semantic.<rule>`，`attributes.docs.detector_layer = semantic`，带 rule / sub_layer / claim / quote（模型：model + prompt_hash + votes）和证据位置；`needs_human = true`，severity 最高 low。

问题优先级（预算有限时先问哪些）：陈旧且带状态词或数值的句子 → 状态词 → 其他陈旧句子 → 数值与列表。

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
| `docsp impact` | 层④a → impact/v1 | 通过/豁免/不适用 → 0；缺文档 → 1 |
| `docsp audit` | 层④c → findings/v1（提议） | 0（不因 finding 失败）；用法/运行错误 → 2 |
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
| 命令注入 | 不用 shell；git 只经 argv 调用且子命令白名单（diff/log/rev-parse/ls-files）；ref 正则校验且禁止 `-` 开头和 `..`；golangci 的 forbidigo 规则禁止 `exec.Command("sh"/"bash")` |
| PR 描述注入（Action） | 经环境变量 → 文件，不拼进脚本 |
| 外部抽取器 | 绝对路径、无 shell、最小环境、超时、输出上限、契约校验 |
| 秘密泄露 | 秘密/私密文件只读 0600（`internal/secret`）；错误信息不含内容；输出文件 0600 |
| 提示注入（④b/④c） | 仓库内容是数据不是指令；AI 只产出提议；提议再过层③；状态变更只有人能做 |
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

## 10. 路线图

| 阶段 | 内容 | 退出条件 |
|---|---|---|
| P0（本轮） | 层③ + 层④a + 契约 + Action + 基准 | 测试/lint/fuzz/vuln 全绿；基准可复现 |
| P1 | security-platform 合并 findings 1.3 扩展；docs findings 进同一 ledger 与看板；SLO 规则由 slo-platform 生成 | 3 个仓库每日检查 ≥ 2 周，新鲜度 SLO 达标 |
| P2 | 层① 生成器（CLI flag 表、环境变量表、指标表）；PR 评论适配器（GitHub/Gitea） | 至少 1 个仓库的参考表由生成器维护 |
| P3 | 层② 可执行文档（一次性容器执行器） | 标注块在 CI 中稳定运行 |
| P4 | ④b 合并后 AI 提议（草稿 PR）；④c 周期审计 findings（带证据，人确认才计入 SLO）——④c 第一版已实现（§3.6、§9.1） | 在 ground truth 上语义类召回 ≥ 50%，被人采纳率 ≥ 60%（当前测试集 15%，未达到） |
| P5 | 更多抽取器（Java/Kotlin、Rust、OpenAPI、Helm 模板引用、Ansible tag）；外部抽取器样例仓库 | 每个新抽取器自带 fixture + fuzz |

## 11. 待产品负责人决定的事项

1. **findings 契约扩展的落地顺序**：是否同意先在 security-platform 合并 1.3（layer `docs` + `docs_` 指标前缀 + `attributes.docs`），再让 docs findings 进共用 ledger？还是 docs-platform 先用独立 ledger 实例？
2. **指标前缀**：只加 `docs_`，还是改成"按产品登记的前缀表"（以后 slo-platform 等也能用）？
3. **`undocumented` 的默认策略**：默认全部公开符号都要求文档（现状，信息量大、噪声也大），还是默认只要求 CLI flag/命令 + 环境变量，其余按仓库在 `reverse.kinds` 里打开？
4. **注释算不算文档**：现在注释里的路径/标识符会被检查（基准里 1/3 的问题在注释里），但也引入了更多 low 置信度噪声。是否保持默认开启？
5. **闸门阈值**：CI 默认 `fail-on medium`（只挡高置信度的 wrong/obsolete）是否合适？
6. **残留词 denylist 的存放位置**：放在私有数据仓库（与 findings 同级，0600），由实例配置指向；是否接受？
7. **AI provider**：已决定——只用本地弱模型（Ollama），产品里没有云端 provider，客户端拒绝非本地地址。待定：每周审计的请求预算（现默认 300 次/仓库）与是否把 `stale_claim` 放进台账（精度 6–20%，建议只做看板信号，不进 SLO）。
9. **④c 的下一步**：P4 退出条件（语义召回 ≥ 50%）用弱模型 + 当前问题形式达不到。可选：(a) 增加确定性规则覆盖（叙述性"过时"大多是"文档描述的计划已合入"——可以用提交信息/PR 标题与文档标题的对应来判定）；(b) 换问题形式（"证据里有没有断言没提到的成员"之类的补全问题，同样要求逐字引用）；(c) 接受 ④c 只是"低成本的提示器"，召回目标下调。
8. **发布**：仓库何时公开、许可证 Apache-2.0 是否最终确定、模块路径 `github.com/MIERAI/docs-platform` 是否沿用。
