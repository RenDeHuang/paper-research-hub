# PubMed 期刊三年回填与每日同步运行手册

## 1. 范围

本阶段只处理期刊注册表中 `resolved + pubmed_supported=yes` 的期刊：

- 首次回填：`2023-07-19` 至 `2026-07-19`，按 PubMed publication date；
- 每日同步：运行日及之前两天，依次查询 `EDAT` 与 `MDAT`；
- 论文身份：DOI 优先，无 DOI 时使用 PMID；
- 同一窗口重复执行由 ingestion job 和 DOI/PMID identity 幂等处理。

本阶段不接 OpenAlex、出版社 API、RSS、预印本或内部定时器。

## 2. 前置条件

### 2.1 期刊输入

需要三份已授权的源名单：

```text
/Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-medicine-wechat.csv
/Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-biology-wechat.csv
/Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-computer-science-wechat.csv
```

三份文件合计必须解析为 2032 条逻辑数据行。命令按传入顺序保留来源顺序。

### 2.2 NCBI 配置

NCBI E-utilities 不要求付费 API key，但必须提供有效的工具名和联系邮箱：

```bash
export NCBI_TOOL='paper-hub'
export NCBI_EMAIL='your-real-email@example.com'
# 可选：export NCBI_API_KEY='...'
```

不要把 `NCBI_API_KEY` 写入仓库、报告或命令日志。

### 2.3 数据库

`sync pubmed-journals` 会写入现有 PostgreSQL ingestion、paper 和 venue 投影，运行前必须：

1. 启动 PostgreSQL；
2. 完成项目现有 migrations；
3. 设置 `DATABASE_URL`。

注册表生成命令本身不需要 `DATABASE_URL`。

## 3. 生成 PubMed 覆盖注册表

注册表生成会复用或建立 Crossref 期刊目录缓存，并用每本已精确解析期刊的全部 ISSN 做一次精确 PubMed OR 查询。

```bash
go -C services/core run ./cmd/journal-pubmed-registry \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-medicine-wechat.csv \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-biology-wechat.csv \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-computer-science-wechat.csv \
  --cache-dir /Users/huangrende/Documents/论文自媒体/期刊名单/enrichment-cache \
  --output data/venues/journal-pubmed-registry.v1.csv \
  --report data/venues/journal-pubmed-registry.v1.report.json
```

成功后应存在：

```text
data/venues/journal-pubmed-registry.v1.csv
data/venues/journal-pubmed-registry.v1.report.json
```

验收重点：

- CSV 数据行恰好 2032；
- 每行都有 `resolved/ambiguous/unresolved` 和 `yes/no/unknown` 的明确状态；
- `pubmed_supported=yes` 的行有正数 `pubmed_record_count`；
- `ambiguous/unresolved` 行不会发起 PubMed 查询；
- report 中输入 bytes、SHA256、行数、CSV 行数和状态统计相互一致。

## 4. 首次三年回填

```bash
go -C services/core run ./cmd/worker -- sync pubmed-journals \
  --mode backfill \
  --registry data/venues/journal-pubmed-registry.v1.csv
```

回填规划规则：

1. 先查询完整三年窗口；
2. 小于 10000 条时直接抓取；
3. 达到 10000 条时按自然年拆分；
4. 单年仍达到 10000 条时按自然月拆分；
5. 单月达到 10000 条时明确失败，不按天猜分、不静默截断。

每个期刊和每个日期窗口都是独立 ingestion job。单项失败会进入最终 JSON 报告，后续期刊继续处理，命令最后返回非零退出码。

## 5. 每日同步

每日任务由外部 Scheduler、CronJob 或 cron 调用；应用内部不维护定时器。运行日期必须显式传入，并按 UTC civil date 解释。

例如在 `2026-07-19` 运行：

```bash
go -C services/core run ./cmd/worker -- sync pubmed-journals \
  --mode daily \
  --run-date 2026-07-19 \
  --lookback-days 3 \
  --registry data/venues/journal-pubmed-registry.v1.csv
```

每本期刊严格生成两个窗口，顺序为：

```text
EDAT: 2026-07-17..2026-07-19
MDAT: 2026-07-17..2026-07-19
```

如果某个窗口的 PubMed count 达到 10000，任务会明确失败并报告，不会只抓取前 10000 条。

Linux cron 示例：

```cron
0 2 * * * cd /app/paper-research-hub && \
  /app/paper-hub-worker sync pubmed-journals \
  --mode daily \
  --run-date "$(date -u +\%F)" \
  --lookback-days 3 \
  --registry data/venues/journal-pubmed-registry.v1.csv \
  >> /var/log/paper-hub/pubmed-journal-daily.log 2>&1
```

云平台 Scheduler 或 Kubernetes CronJob 只需要调用同一条命令，不要复制窗口规划逻辑。

## 6. 幂等、重试与报告

每个窗口的幂等 key 绑定：

- 完整排序 ISSN 集；
- 日期类型（publication/entrez/modification）；
- inclusive `from` 和 `to` 日期。

因此三天重叠不会依赖标题去重；同一 PMID 的后续记录会更新既有 Work。重复执行同一成功窗口不会创建新的论文。

运行结果包含：

- registry/eligible/no-coverage 期刊数；
- 计划、尝试、成功、失败的期刊和窗口数；
- `raw_inserted`、`raw_reused`、`projected`、`excluded`、`deleted`、`unchanged`、`failed`；
- 每个期刊、窗口和失败阶段的明细。

即使命令返回非零，worker 也会先输出 JSON 报告，再将错误写入 stderr。重试时直接重新执行相同命令；已完成窗口由 ingestion 幂等键复用，失败窗口会再次产生可审计结果。

## 7. 当前工作区验证边界

无需外部服务即可验证：

```bash
go -C services/core test ./internal/paper ./internal/source/pubmed \
  ./internal/venueenrich ./internal/pubmedsync \
  ./cmd/journal-pubmed-registry ./cmd/worker -count=1
go -C services/core test -race ./internal/source/pubmed \
  ./internal/venueenrich ./internal/pubmedsync -count=1
go -C services/core vet ./internal/paper ./internal/source/pubmed \
  ./internal/venueenrich ./internal/pubmedsync \
  ./cmd/journal-pubmed-registry ./cmd/worker
```

真实生成注册表和真实回填还需要：

- 有效 `NCBI_EMAIL`（`NCBI_TOOL` 可自定义）；
- 可访问的 Crossref/NCBI 网络；
- 生成注册表时的 Crossref cache 目录；
- 回填时可用的 PostgreSQL。
