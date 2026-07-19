# 2032 本期刊 PubMed 三年回填与每日同步设计

**日期：** 2026-07-19  
**状态：** 用户已批准  
**范围：** 第一阶段仅完成 PubMed 业务闭环

## 1. 目标

以现有 2032 行期刊候选名单为入口，确定性获得 ISSN 后：

1. 检测每本期刊是否存在 PubMed 记录；
2. 对 PubMed 覆盖的期刊导入 2023-07-19 至 2026-07-19 发表的论文；
3. 每天运行一次增量同步；
4. 保存标题、作者、摘要、期刊、发表日期、PMID、DOI；
5. 有 DOI 时提供 DOI 链接，无 DOI 时保留 PubMed 链接；
6. 输出覆盖、成功、未解析和失败统计。

本阶段不接 OpenAlex、出版社 API、RSS、预印本或计算机会议数据。

## 2. 复用现有工程能力

继续使用项目现有组件，不建立平行采集系统：

- `internal/source/pubmed`：ESearch、EFetch、XML 解析；
- `internal/ingestion`：原始记录、规范化、幂等投影和任务审计；
- PostgreSQL：`venues`、`works`、`source_records`、`external_identifiers`、
  `authors`、`work_authors` 和 ingestion 状态；
- `cmd/worker`：现有 `sync pubmed` 单次同步入口。

PubMed 记录中的 ISSN 由现有 ingestion 投影逻辑解析或创建 Venue。期刊候选注册表是同步任务的受控输入，
不新增一套平行 Venue 数据模型。

## 3. 期刊注册表

第一阶段生成稳定 CSV，一行对应一条候选记录，至少包含：

```text
domain
source_order
source_journal_name
issn_l
print_issn
eissn
all_issns
crossref_publisher
resolution_status
pubmed_supported
pubmed_record_count
pubmed_checked_at
source_url
```

仅 `resolution_status=resolved` 且具有合法 ISSN 的记录允许探测 PubMed。

状态语义：

- `yes`：精确 ISSN 查询成功且记录数大于 0；
- `no`：精确 ISSN 查询成功且记录数为 0；
- `unknown`：网络、限流或响应验证失败；
- `ambiguous/unresolved`：期刊身份未唯一确定，不查询 PubMed。

## 4. PubMed 查询模式

### 4.1 覆盖探测

对每本已解析期刊，把全部已知 ISSN 组成精确 OR 查询，使用 ESearch `retmax=0`，只读取 count。

### 4.2 首次三年回填

范围固定为：

```text
2023-07-19 至 2026-07-19
```

按 PubMed publication date 查询。单个日期窗口命中数未超过 PubMed 可返回上限时直接抓取；
达到上限时按自然年拆分，单年仍达到上限时按自然月拆分。所有窗口互斥且覆盖完整范围。

### 4.3 每日增量

每天查询运行日及之前两天，共三天重叠窗口：

- `EDAT`：发现新进入 PubMed 的记录；
- `MDAT`：发现最近被修改的记录。

重叠窗口依赖 PMID 幂等写入，不使用精确到小时的水位，也不根据标题去重。

## 5. 论文身份和链接

- 有 DOI：现有 DOI 规范化继续作为优先 canonical identity；
- 无 DOI：PMID 必须允许作为 canonical identity，防止丢弃合法 PubMed 记录；
- DOI 链接由规范 DOI 生成；
- 无 DOI 时使用 PubMed PMID 链接。

PMID 和 DOI 都写入 `external_identifiers`。同一 PMID 的后续 MDAT 记录更新既有 Work，不创建重复论文。

## 6. 业务编排

新增一个注册表级 worker 命令，负责顺序读取已支持期刊并调用现有 PubMed 客户端：

```text
sync pubmed-journals --mode coverage
sync pubmed-journals --mode backfill
sync pubmed-journals --mode daily
```

每个期刊和日期窗口形成独立 ingestion job，使用确定性的幂等键。单本期刊失败不会阻断其他期刊；
最终命令在报告中列出失败项并返回非零状态。

## 7. 调度

应用内部不维护定时器。部署后由外部调度器每天调用一次：

```text
sync pubmed-journals --mode daily --lookback-days 3
```

本地 cron、云平台 Scheduler 或 Kubernetes CronJob 都调用同一命令。

## 8. 输出和验收

第一阶段必须交付：

1. 2032 行期刊 PubMed 覆盖注册表；
2. 2023-07-19 至 2026-07-19 的首次回填能力；
3. 每日三天重叠增量能力；
4. DOI 和 PMID 链接；
5. 每次运行的成功、插入、更新、无覆盖、未解析和失败统计；
6. 重复运行不创建重复论文；
7. 不使用模糊标题匹配，不静默跳过失败。

