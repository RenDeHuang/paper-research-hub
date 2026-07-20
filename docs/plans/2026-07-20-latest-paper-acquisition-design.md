# 最新论文采集主链路设计

**日期：** 2026-07-20  
**状态：** 用户已批准  
**目标：** 以目标期刊、预印本服务器和会议注册表为范围，每天发现最新论文并保存可验证元数据。

## 1. 冻结范围

产品继续覆盖四个频道：

```text
journal_published
accepted_early
preprint
conference_proceeding
```

本轮先完成期刊论文的可运行闭环：

```text
目标期刊 ISSN Registry
→ PubMed / Crossref 最新记录
→ 精确身份校验
→ PostgreSQL ingestion
→ Catalog / API
```

本轮不开发逐期刊 DOM 爬虫，不以历史 PubMed 覆盖探测阻塞日更，不新增 Kubernetes、
队列或内部常驻调度器。

## 2. 来源优先级

### 2.1 正式期刊和 Early Access

1. Crossref `created` 负责通用 DOI 新记录发现；
2. Crossref `updated/deposited` 负责已有记录更新；
3. PubMed `EDAT` 负责发现最近进入 PubMed 的医学、生物学记录；
4. PubMed `MDAT` 负责摘要、DOI、勘误、撤稿和其他记录变化；
5. 通用来源出现可复核缺口后，才按出版社接 API、RSS 或 TOC。

PubMed 缺失不等于期刊没有新论文，也不能把期刊永久排除。出版社官网接入按出版社
connector 实现，不按 2032 本期刊分别实现。

### 2.2 后续频道

- 预印本：bioRxiv/medRxiv API、arXiv API/OAI-PMH；
- 会议论文：CCF A/B Registry 加 Crossref/OpenAlex/IEEE/ACM 官方来源。

这两类在期刊日更主链路验收后独立接入，不与当前 PubMed 改造混在同一个提交中。

## 3. PubMed 日更语义

### 3.1 Registry 资格

所有 `resolution_status=resolved` 且具有严格有效 ISSN 集的期刊进入日更。字段
`pubmed_supported=yes/no/unknown` 只保存某次历史审计结果，不是日更资格条件。

`ambiguous` 和 `unresolved` 不进入自动查询，因为没有可靠期刊身份。

### 3.2 日期窗口

每天按显式 UTC `run-date` 查询包含当天在内的固定三日窗口：

```text
EDAT: D-2 .. D
MDAT: D-2 .. D
```

窗口重叠通过 PMID 幂等更新处理。发表日期用于展示，不能替代 EDAT 作为“最新进入
PubMed”的发现时间。

### 3.3 确定性批次

2026-07-20 对真实 Registry 的只读验证结果：

```text
resolved unique ISSNs = 2689
single POST body       = 62001 bytes
single POST result     = rejected
reason                 = Boolean operators exceed 2048
```

2048 个 ISSN term 加日期过滤的 POST 已真实成功。因此日更批次遵循：

1. 期刊按 Registry 来源顺序稳定排列；
2. 同一本期刊的全部 ISSN 必须留在同一批次；
3. 每批最多 2048 个唯一 ISSN term；
4. 每个批次分别执行 EDAT、MDAT；
5. ESearch 使用 `POST`、`usehistory=y`、`retmax=0`；
6. EFetch 使用返回的 `WebEnv` 和 `QueryKey` 分页抓取 XML。

不根据标题、出版社名称或近似字符串拆分、合并或补救。

## 4. 记录校验和身份

EFetch 返回记录后必须满足：

1. 返回 Venue 的 ISSN、ISSN-L 或明确 ISSN assertion 与 Registry 精确相交；
2. PubMed 内部身份使用 PMID；
3. DOI 只使用来源中的明确 DOI assertion；
4. 同一 PMID 在 EDAT、MDAT或不同运行中重复出现时执行幂等更新；
5. 无摘要、无 DOI 等可选字段保持缺失，不生成替代值。

保存字段包括 PMID、DOI、标题、摘要、作者、期刊、ISSN、publication history、
正式发表日期、电子发表日期、Publication Type、勘误/撤稿关系和来源证据。

## 5. 执行与持久化

API 抓取本身不依赖历史覆盖表，但上线产品仍使用现有 PostgreSQL ingestion：

```text
Search/Fetch
→ raw source record
→ deterministic normalization
→ PMID/DOI identity
→ Work/Venue projection
```

外部 cron、云 Scheduler 或一次性容器任务每天调用同一命令。API 和 Web 启动过程不
隐式抓取数据。

## 6. 本轮验收

1. Registry 日更加载全部 resolved 期刊，不受历史 PubMed yes/no 限制；
2. 真实 2689 个唯一 ISSN 被稳定分成不超过 2048 term 的批次；
3. 每个批次只产生 EDAT、MDAT 两个三日窗口；
4. ESearch 对批量 ISSN 使用 POST；
5. 不再执行每本期刊两次 ESearch；
6. 重复 PMID 不产生重复 Work；
7. 定向测试、race、vet 和真实只读 PubMed 查询通过；
8. 运行手册明确“每天一次、三日重叠、全部 resolved、批量 POST”。
