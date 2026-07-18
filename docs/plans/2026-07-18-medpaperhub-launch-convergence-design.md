# medpaperhub 上线收敛设计

## 目标

medpaperhub 面向医学、生物学、计算机科学科研人员，持续回答：

1. 今天和最近有哪些新论文；
2. 每篇论文大概研究了什么、采用了什么研究路线；
3. 最近哪些主题、技术和研究设计正在增长；
4. 某本期刊最近偏好什么类型的文章；
5. 用户自己的研究与哪些期刊更适配。

产品不是以搜索为首页核心的通用文献库。搜索保留为辅助能力，首页核心是每日科研情报。

## 交付模型

原 23 个工程 Task 不删除，而是归入 9 个一级业务 Task。数据库、去重、任务恢复、
API、前端、部署和测试是一级 Task 的完成条件，不再与业务结果平级汇报。

| 一级 Task | 业务结果 | 主要工程责任 |
| --- | --- | --- |
| 1. 期刊池与频道准入 | 确定收录边界 | JCR/来源 Registry、版本、数据库约束、准入测试 |
| 2. 每日多来源发现 | 稳定发现新论文 | Connector、raw、watermark、幂等、失败恢复 |
| 3. 论文规范记录 | 得到唯一、可追溯的论文 | 字段级来源、去重、Work Family、摘要、发表状态 |
| 4. 官方原文链 | 每篇公开论文都能跳转 | URL 候选、验证、有效期、公开硬门槛 |
| 5. 摘要研究路线 | 知道论文大概怎么做 | 严格结构化模型输出、证据范围、版本、拒绝臆测 |
| 6. 趋势与期刊画像 | 判断什么在增长、期刊偏好什么 | 周/月趋势、12/24 月画像、family 去重、分析快照 |
| 7. 投稿适配 | 用户知道更适合哪些期刊 | 用户摘要结构化、期刊画像匹配、可解释差异 |
| 8. Go API 与 Nuxt 门户 | 科研人员可直接使用 | OpenAPI、Facts/Analysis Catalog、每日情报 UI |
| 9. 本地部署与重放 | 从空库一条命令交付 | deploy、Compose、脚本、健康、备份、严格验收 |

Task 1 的 JCR Q1 核心已经按 `journal-all-q1/v2` 完成；三领域、四频道及预印本/会议
Registry 仍需补齐后才能把 Task 1 标记为整体完成。数据层保留全部授权 JCR Q1；首页
可以提供“Q1 内前半段”优先视图，但它不是第二个数据准入门槛。

## 首版本范围

### 必须来源

- Crossref：正式论文、Accepted/Early、更新发现；
- PubMed：医学和生物学语义、发表历史增强；
- bioRxiv、medRxiv：生命科学和医学预印本；
- arXiv：计算机科学预印本；
- IEEE Xplore、审核过的 ACM feed：计算机会议论文；
- OpenAlex：由稳定 DOI 触发的引用和主题增强。

DataCite、Europe PMC 和更多出版社专用接口不阻塞本地首版上线。

### 四个频道

- `journal_published`
- `accepted_early`
- `preprint`
- `conference_proceeding`

正式期刊和 Accepted/Early 执行 JCR Q1 准入；预印本和会议论文使用独立来源
Registry，不应用 JCR。

## 数据主链路

```text
Registry
→ connector run
→ immutable raw record
→ normalized field assertions
→ Work / Work Family
→ verified official URL
→ Facts Catalog
→ abstract structured analysis
→ analysis readiness
→ trends / journal profiles
→ Analysis Catalog
→ Go API
→ Nuxt
```

每个 connector 使用独立 typed watermark。只有完整批次在同一事务内成功后才能推进
watermark。重放相同来源修订不能产生重复 Work。

## OpenAI 摘要结构化分析

### 配置

第一版使用已经由运行环境提供的：

```text
OPENAI_BASE_URL
OPENAI_API_KEY
OPENAI_MODEL
```

三个配置对摘要分析 Worker 都是必填项。没有默认模型、没有第二供应商、没有静默降级。
API Key 不写入数据库、日志、Catalog、错误正文或 Git。

### API 契约

Worker 使用 OpenAI Responses API 兼容接口和严格 JSON Schema 输出。每个分析 run 保存：

- `model_provider=openai-compatible`
- 实际 `model_name`
- `prompt_version`
- `schema_version`
- 输入摘要的 SHA-256
- 输入标题、摘要及可审计来源引用
- 原始响应 ID
- 输出结构化 JSON
- 每个字段的摘要证据片段或 `not_reported`
- 请求开始、完成时间和最终状态

不保存认证头。网络错误、schema 不匹配、拒答或截断都使该次分析 run 失败，不使用文本
解析、正则修补或第二次非严格输出补救。

### 输出 Schema

第一版输出以下独立字段：

```text
domain
research_problem
research_purpose
research_objects
content_type
research_mode
study_design
data_or_samples
core_methods
technical_route
validation_strategy
main_findings
innovation_points
application_direction
limitations_reported
```

每个字段必须同时包含：

```text
state = supported | not_reported
value
evidence[]
```

`evidence[]` 必须是输入摘要中的原文片段。服务端验证证据确实存在于规范化后的输入摘要；
不存在则拒绝整份结果。模型不能根据常识补齐摘要未报告的信息。

`analysis_scope` 首版固定为 `abstract`。未来全文分析必须使用新的 schema/prompt 版本，
不能覆盖摘要分析。

## 趋势和期刊画像

只有 `analysis_ready=true` 且早于 analysis cutoff 的 Work 进入分析。默认按
`work_family_id` 计数，防止预印本和正式版重复推高趋势。

### 趋势

- 周、月发文量和变化；
- 主题、疾病、技术、研究模式、研究设计分布；
- 技术的 Novelty、Momentum、Diffusion、Maturity 独立结果；
- 保存样本量、覆盖率、效应量、置信区间和多重检验边界。

### 期刊画像

- 12、24 个月窗口；
- 主题、研究目的、研究模式、研究设计、技术路线和文章类型分布；
- 最近增长和下降方向；
- 证据不足时返回 `insufficient_evidence`，不填零。

## 投稿适配

用户提交标题和摘要，系统使用与论文分析相同的 schema/prompt 版本形成结构化研究画像，
再与期刊 12/24 月画像比较。输出独立维度，不生成隐藏综合录用概率：

- 主题适配；
- 研究目的适配；
- 研究模式适配；
- 研究设计适配；
- 技术路线适配；
- 文章类型适配；
- 支持判断的近期论文；
- 明确的不适配点。

## 前端

首页固定优先展示：

1. 今日正式发表；
2. 今日新预印本；
3. 最新会议论文；
4. 最近 Accepted/Online First；
5. 今日新发现；
6. 本周和本月趋势；
7. 期刊动态。

每个公开论文卡片必须使用 API 返回的 active verified official URL。Nuxt 不拼接 DOI
URL、不连接 PostgreSQL、不保存数据源或 OpenAI 凭据。

## 本地部署终点

保留当前根目录 Compose 作为开发入口，新增统一部署目录：

```text
deploy/
├── README.md
├── compose/
├── env/
├── manifests/
├── scripts/
├── sql/
├── systemd/
└── monitoring/
```

最终提供两个明确入口：

```text
make local-up
make local-release MANIFEST=/absolute/path/to/replay.yaml
```

`local-up` 只证明空库服务边界；`local-release` 必须从空库完成 Registry、真实或固定输入、
URL 验证、摘要分析、趋势/期刊画像、Catalog 发布和 Web/API 验证。

`local-release` 缺少授权 JCR、固定输入、必要凭据或 OpenAI 配置时必须失败。它不能使用
synthetic JCR、mock 论文、隐式当前时间或在线结果代替确定性 replay。

## 完成定义

本轮只有在以下条件全部满足后结束：

- Go 测试、race、Vet 通过；
- Nuxt lint、typecheck、unit、build、Playwright 通过；
- Compose 和部署 manifest 校验通过；
- 从全新 PostgreSQL volume 完整发布 Catalog；
- `/api/v1/home` 返回 `200` 和正确 `X-Catalog-Generation`；
- 首页同时出现正式论文、预印本、会议论文、Accepted/Early 和新发现；
- 所有公开论文都有 active verified official URL；
- 相同固定输入重放不产生重复 Work；
- 摘要结构化结果满足严格 Schema 和证据子串校验；
- 周/月趋势、12/24 月期刊画像可复算；
- 用户摘要可以生成带证据说明的期刊适配结果；
- `deploy/README.md` 可以由新环境操作者独立复现。
