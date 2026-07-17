# medpaperhub 医学生物学论文情报门户设计

**日期：** 2026-07-17  
**状态：** 用户已批准  
**产品名称：** `medpaperhub`

## 1. 产品定义

`medpaperhub` 是面向医学、生物学教授、课题组负责人、临床科研人员和研究生的论文与期刊情报门户。它不追求收录所有学科，也不把抓到的全部元数据直接公开，而是只公开满足以下准入条件的医学、生物学期刊论文：

```text
JCR Q1 OR JIF >= 10
```

每个公开判断必须绑定：

- 明确的期刊实体和 ISSN 证据；
- JCR 指标年份；
- JCR 学科分类；
- JIF 精确值和 Quartile；
- 授权数据来源；
- 版本化 policy；
- 可审计的 `accepted` 判定。

没有授权 JCR 证据、ISSN 无法确定、判定为 `unknown`、`rejected` 或 `not_applicable` 的记录不得进入公开 Catalog。系统保留其原始记录和内部审计状态，但不能把它们展示成公开精选论文。

## 2. 用户要回答的问题

### 2.1 学科情报

教授进入自己的医学或生物学领域后，应能够回答：

- 最近 24 小时、7 天、30 天发表了哪些论文？
- 哪些期刊最近在该领域持续发文？
- 哪些疾病、靶点、通路、技术和研究设计正在增长？
- 哪些论文引用增长快，而不是仅仅累积引用高？
- 哪些研究方向存在外部验证、RCT、多中心研究或数据开放缺口？

### 2.2 期刊情报

期刊页面应回答：

- 最近发表了什么文章？
- 过去 12 个月和 24 个月的主题、疾病、方法和 Article Type 分布如何？
- 相对于同领域期刊，该期刊显著富集哪些选题和研究设计？
- JCR 指标年份、JIF、全部相关 Quartile 和准入规则是什么？
- 这些模式由多少篇论文支持，覆盖时间和缺失率是多少？

页面使用“编辑与选题模式”表述历史发表模式，不宣称知道编辑部主观偏好。

### 2.3 论文情报

论文详情页应提供：

- 标题、摘要、作者、机构、期刊和发表时间；
- PMID、PMCID、DOI 等规范标识；
- MeSH Descriptor、Qualifier、Major Topic；
- Publication Type 和研究设计；
- 引用总量、引用速度、同领域同年份百分位；
- 引用数据来源和快照时间；
- 开放全文与许可；
- 数据集、代码、临床试验和相关版本；
- 来源字段证据和缺失状态。

## 3. 信息架构

### 3.1 首页

首页品牌固定为 `medpaperhub`，首页不是通用搜索页，而是医学生物学研究情报总览：

1. 今日新增精选论文；
2. 7 天增长最快的学科；
3. 高引用增长论文；
4. 最近活跃期刊；
5. 热门疾病、靶点、方法和研究设计；
6. 证据缺口明确的研究机会；
7. 数据更新时间、JCR 指标年份和覆盖范围。

每个榜单都必须显示统计窗口、样本量、数据来源和缺失状态。

### 3.2 学科页面

第一层学科采用稳定、版本化的医学生物学 taxonomy。论文归类以 MeSH Tree Number 为核心证据，并允许使用 JCR Category 作为期刊层级的补充分类。首批导航领域包括：

- 肿瘤学；
- 免疫学；
- 神经科学；
- 心血管；
- 代谢与内分泌；
- 感染与微生物；
- 遗传与基因组学；
- 细胞生物学；
- 分子生物学；
- 药理学与药物发现；
- 生物信息学；
- 转化医学。

这些是导航聚合，不替换原始 MeSH Descriptor、Qualifier 和 Tree Number。一个论文可以属于多个领域，归类关系必须保留来源和 taxonomy 版本。

### 3.3 期刊页面

新增独立的 `/journals` 和 `/journals/{slug}` 页面。期刊详情包括：

- 规范名称、别名、Publisher、ISSN-L、ISSN、eISSN；
- JCR 指标年份；
- 精确 JIF；
- 所有 JCR Category 和 Quartile；
- 命中的准入规则；
- 最近论文；
- 主题、MeSH、Publication Type 和方法分布；
- 相对于同领域基线的富集分析；
- 发表节奏和趋势；
- 数据覆盖和缺失说明。

### 3.4 趋势与研究机会页面

趋势页面拆分为：

- 论文趋势；
- 学科趋势；
- 疾病与靶点趋势；
- 方法与技术趋势；
- 期刊趋势；
- Publication Type 和研究设计趋势。

研究机会不是由语言模型自由生成。第一阶段只发布由结构化证据触发、可复算的机会，例如：

- 论文数量快速增长但 RCT 占比低；
- 单中心研究较多但外部验证不足；
- 高引用主题中数据或代码开放率低；
- 某疾病研究集中于观察性研究，缺少前瞻性研究；
- 某技术在同领域增长显著，但独立团队数仍少。

## 4. 数据源职责

### 4.1 PubMed

PubMed 是医学论文发现和医学语义的主来源，负责：

- PMID、PMCID、DOI；
- 标题、结构化摘要；
- MeSH Heading、Qualifier、Major Topic；
- Publication Type；
- 作者、机构；
- 修订、撤稿和关联出版物；
- 期刊标识和发表日期。

现有解析器已经读取部分字段，但规范化、投影和 Catalog 还没有完整保存 MeSH、Publication Type、结构化摘要和关系信息，必须补齐。

### 4.2 Europe PMC

新增 Europe PMC connector，负责医学引用关系和可获得的开放科学元数据：

- citation count 和引用关系；
- reference list；
- 开放全文链接；
- 资助信息和可用的文本挖掘实体；
- PMID、PMCID、DOI 对齐。

Europe PMC 数据必须作为独立 source record 保存，不能覆盖 PubMed 原始断言。字段获胜规则必须版本化并可审计。

### 4.3 PMC Open Access

PMC OA 仅用于明确允许复用的全文：

- 保存 OA 许可和来源；
- 原始全文资产与解析产物分离；
- 无明确复用许可时不得发布全文；
- 全文可用性不能冒充浏览量或下载量。

### 4.4 Crossref

Crossref 用于：

- DOI、ISSN 和 Publisher 增强；
- online/print publication date；
- license、relation 和版本关系；
- Crossref 范围内的引用计数。

Crossref 引用计数必须标记来源，不能与其他来源计数无标签混合。

### 4.5 OpenAlex

OpenAlex 用于补充：

- cited-by count；
- counts by year；
- 机构和作者标识；
- broader citation graph。

OpenAlex 数据必须以快照保存。没有 API key 时对应字段保持 `missing`，不能回退为推断值。

### 4.6 JCR

JCR 数据只从用户明确授权的 CSV 导入：

- 不抓取来源不明的第三方 JCR 表；
- 不把 SJR、CiteScore 或 OpenAlex citedness 标成 JIF；
- 不把合成 fixture 当作生产数据；
- 导入、指标、policy assessment 和 Catalog 发布形成完整审计链。

## 5. 规范数据模型

现有 `source.Record` 和 PostgreSQL schema 需要扩展或接线以下实体：

- `mesh_descriptors`
- `mesh_qualifiers`
- `work_mesh_headings`
- `publication_types`
- `work_publication_types`
- `journal_categories`
- `work_journal_context`
- `citation_snapshots`
- `citation_edges`
- `reference_edges`
- `analysis_cohorts`
- `journal_pattern_snapshots`

所有来源断言保留 source record、source path、快照时间和 policy version。MeSH UI 和 Tree Number 是稳定标识，显示名称不是唯一键。

引用数据采用 source-specific snapshot：

```text
(work_id, source, observed_at, citation_count)
```

不建立一个无来源、可被任意覆盖的“最终引用数”。公开页面可以选择一个明确的主展示来源，但必须同时显示来源和更新时间。

## 6. 期刊准入与 Catalog 硬门槛

JCR 导入后必须显式运行版本化 assessment：

```text
accepted = any(category.quartile == Q1) OR any(category.jif >= 10)
```

Catalog 发布只允许满足以下条件的 Work：

1. 至少一个当前 source state 为 `included`；
2. Work、source record、normalization 和 projection provenance 完整；
3. Venue 已通过严格 ISSN 关联；
4. 最新适用 policy assessment 为 `accepted`；
5. assessment 的指标年份属于本次 Catalog generation 声明的 JCR 数据版本；
6. Work 生命周期不是 rejected、retracted 或 withdrawn；
7. 论文属于医学或生物学范围。

`missing`、`unknown`、`rejected` 和 `not_applicable` 一律不进入公开 generation。发布器必须在数据库事务内验证，不允许只在前端过滤。

## 7. 引用与趋势指标

### 7.1 引用指标

公开展示的引用指标包括：

- source-specific citation count；
- 30、90、180 天引用增量；
- citations per day/month；
- 同领域、同年份、同 Publication Type cohort 百分位；
- 引用加速度；
- citation coverage 和 missing state。

每项指标必须绑定公式版本、观察时间、cohort 定义和样本量。

### 7.2 趋势检测

趋势不使用不可解释的单一隐藏分数。第一阶段采用：

- 按周和月统计论文量；
- 最近窗口与历史基线的 rate ratio；
- 置信区间；
- 最小样本量约束；
- 多重比较校正；
- 独立期刊数和独立团队数；
- 结果生成时间和公式版本。

对于 count data，优先采用 Poisson 或 Negative Binomial 模型，并根据离散程度选择预先声明的模型。不能在结果不显著后临时更换模型。

### 7.3 期刊编辑与选题模式

期刊模式分析以同 JCR Category、同时间窗口的期刊为基线，输出：

- MeSH、疾病、靶点、方法、Publication Type 富集；
- odds ratio 或 rate ratio；
- 置信区间；
- 支持论文数；
- 覆盖率；
- 多重比较校正结果。

不将统计关联写成因果偏好。

### 7.4 下载指标

系统不创建跨出版社的虚构下载量。只有在来源提供明确、可审计、可比较的 article-level usage metric 时才保存：

```text
(work_id, source, metric_name, observed_at, value, definition)
```

否则公开状态为 `missing`。全文可用、引用数、Altmetric 或收藏数不能替代下载量。

## 8. API 设计

保留前后端独立部署，新增或扩展：

```text
GET /
GET /api/v1/stats
GET /api/v1/papers
GET /api/v1/papers/{id}
GET /api/v1/disciplines
GET /api/v1/disciplines/{slug}
GET /api/v1/journals
GET /api/v1/journals/{slug}
GET /api/v1/trends/papers
GET /api/v1/trends/disciplines
GET /api/v1/trends/journals
GET /api/v1/trends/mesh
GET /api/v1/research-opportunities
```

`GET /` 返回 API discovery JSON，不重定向到 Nuxt，也不托管前端。

所有公开响应继续绑定 immutable Catalog generation。筛选参数必须包含 JCR 指标年份和准入状态的可验证语义，但公开 Catalog 不提供绕过 `accepted` 门槛的参数。

## 9. 前端设计方向

品牌文字统一为 `medpaperhub`，保留当前高密度研究情报界面，但调整信息层级：

- 一级导航：首页、学科、期刊、论文、趋势、研究机会；
- 首页优先显示“今天有什么新研究”，而不是通用 Agent 搜索；
- 卡片突出期刊、学科、Publication Type、发表时间和引用增长；
- JCR badge 显示指标年份和命中规则；
- 所有数字可展开查看来源、时间窗口和覆盖率；
- `unknown`、`missing` 和 `0` 保持区别；
- 移除 AI Agent 专用文案和示例；
- 空状态明确说明缺少 JCR 数据、尚未同步或尚未发布，而不是笼统失败。

## 10. 运行与部署

保持现有解耦：

- Nuxt Web 独立镜像；
- Go API 独立进程；
- Go Worker 作为一次性 Job；
- PostgreSQL 作为规范存储；
- JCR import、source sync、analysis 和 Catalog publish 分开调度；
- API 启动不隐式抓取、不隐式分析、不隐式发布。

生产调度顺序：

```text
同步 PubMed/Crossref/Europe PMC/OpenAlex
→ 规范化与 Work 收敛
→ 导入授权 JCR
→ 生成 Venue policy assessment
→ 构建引用快照与分析 cohort
→ 运行趋势和期刊模式分析
→ 发布 Catalog generation
→ Web/API canary 验证
```

## 11. 错误处理与真实性边界

- 数据源失败保留 job 和阶段错误，不发布部分伪成功；
- 引用来源冲突时并列保存，不静默覆盖；
- Venue 无法按 ISSN 确定时不使用标题模糊匹配进入公开 Catalog；
- JCR 年份不一致时拒绝发布；
- 趋势样本量不足时返回 `insufficient_evidence`；
- 无下载量时返回 `missing`；
- 无全文复用许可时只展示元数据链接；
- 研究机会必须列出触发规则、支持论文和限制。

## 12. 验收标准

### 数据与准入

- 授权 JCR CSV 可以从空数据库导入；
- 每个已匹配 Venue 产生版本化 assessment；
- `accepted` 的定义严格等于 `JCR Q1 OR JIF >= 10`；
- `missing`、`unknown`、`rejected`、`not_applicable` 不进入 Catalog；
- DOI/PMID/PMCID 跨来源收敛保持可审计；
- PubMed MeSH 和 Publication Type 可以从 API 查询并在页面显示。

### 用户页面

- 首页品牌显示 `medpaperhub`；
- 首页展示真实医学生物学论文、学科、期刊和趋势；
- 学科页面回答最近论文、活跃期刊和增长主题；
- 期刊页面显示 JCR 证据、最近论文和编辑与选题模式；
- 论文页显示医学语义、引用快照和来源；
- 页面不存在 AI Agent 示例或 mock 数据。

### 分析

- 引用速度能够从至少两个时间快照复算；
- 趋势结果包含窗口、基线、样本量、估计值和不确定性；
- 期刊富集结果绑定同领域基线；
- 样本不足不生成肯定结论；
- 下载量缺失不会被其他信号替代。

### 工程与部署

- `GET /` 返回 API discovery JSON；
- Go、Nuxt、OpenAPI、Playwright 和容器烟测全部通过；
- 从空数据库可以按声明顺序完成同步、assessment、analysis 和 Catalog 发布；
- API 和 Web 继续使用独立镜像、端口和扩缩容边界；
- 公开 Catalog generation 可以从数据库审计到全部来源和公式版本。

## 13. 非目标

第一阶段不实现：

- 全学科论文门户；
- 未授权 JCR 抓取；
- 统一或推断的跨出版社下载量；
- 基于标题相似度的 Venue 准入；
- 语言模型自由生成的研究机会；
- 自动投稿成功率预测；
- 将统计发表模式表述为编辑部因果偏好。

