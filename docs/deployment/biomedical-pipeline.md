# Biomedical Catalog 部署与数据流水线

本文只描述当前仓库已经实现、可由 `services/core/cmd/worker/main.go` 解析并执行的命令。不存在的命令会明确标注，不能写入生产调度。

## 不变量

### 唯一公共 Venue 门槛

公开 Catalog 的正式期刊准入仅接受授权 JCR Q1 证据：

```text
任一授权 JCR Category 为 Q1
```

- 固定策略身份：policy name=`journal-all-q1`、policy revision=`2`；完整 policy version/label=`journal-all-q1/v2`。
- `Q1` 指同一 Venue、明确 `metric_year` 下任一授权 JCR Category 的 Quartile。
- JIF、JIF percentile、JIF rank 和 category journal count 只作为授权 JCR 证据字段，不参与准入阈值。
- Biomedical Subject exact-link、Work/Venue/provenance 完整性和 active 状态是公开范围与证据完整性要求，不是其他质量阈值。
- 不允许以引用数、下载数、OpenAlex `2yr_mean_citedness`、标题相似度或推断 Quartile 替代 JCR。

### 禁止 synthetic JCR 发布

`data/venues/jcr-q1.example.csv` 只用于测试 schema、JCR 证据字段和策略边界。它的标题、ISSN、Category 和指标都是合成数据，禁止：

- 作为生产 `JCR_IMPORT_PATH`；
- 挂载到生产 Worker；
- 参与 Venue assessment；
- 用于发布任何生产 Catalog generation。

生产 JCR CSV 必须由用户明确授权，并通过 `JCR_SOURCE_LICENSE` 记录可审计的授权或许可依据。

## 部署边界

| 组件 | 生命周期 | 允许职责 | 禁止职责 | 主要配置 |
| --- | --- | --- | --- | --- |
| PostgreSQL | 有状态、持久化 | 唯一规范存储；保存 ingestion、JCR、Subject、assessment、eligibility 和 immutable Catalog snapshots | 不执行抓取；不托管 Web；生产环境不直接暴露公网 | `DATABASE_URL`；生产备份与恢复策略 |
| `paper-hub-migrate` | 一次性 Job | 仅执行 `up`，按 checksum 应用镜像内嵌迁移 | 不随 API 启动隐式运行；不抓取、不发布 | `DATABASE_URL` |
| `paper-hub-api` | 常驻服务 | 打开 PostgreSQL pool；读取已发布 Catalog；提供 discovery、health 和 `/api/v1/*` | 不迁移、不同步、不导入、不 assessment、不发布；不托管 Nuxt | `DATABASE_URL`、稳定的 `CATALOG_CURSOR_SECRET`、`API_*` |
| `paper-hub-worker` | 一次性 Job | 显式执行 sync/import/assess/analyze/publish | 不作为无参数常驻进程；不提供 HTTP health | `DATABASE_URL` 加当前命令所需的最小凭据 |
| Nuxt Web | 独立常驻服务 | SSR/浏览器 UI；通过 HTTP 调用 Go API | 不连接 PostgreSQL；不持有 JCR/NCBI/数据库凭据；不生成 fixture 数据 | SSR 用 `INTERNAL_API_BASE_URL`；浏览器用 `NUXT_PUBLIC_API_BASE_URL` |

Core 镜像同时包含：

```text
/app/paper-hub-api
/app/paper-hub-worker
/app/paper-hub-migrate
```

共享镜像不代表共享生命周期。生产编排必须把 API、Migrate 和每个 Worker invocation 建模为独立 workload。

本地 Compose 固定使用 `postgres:18.4-alpine3.23`。生产 PostgreSQL 18 实例必须先由独立 Migrate Job 应用同一 Core 镜像内嵌迁移，再接收 API 或 Worker 流量。

## 从空库到公开 Catalog：严格顺序

以下命令从仓库根目录执行。示例时间是确定性 RFC3339Nano 输入，不表示命令会自动使用当前时间；生产运行必须替换为该批次实际声明的时间。

### 0. 准备变量

```bash
cp .env.example .env

export SUBJECT_VERSION='biomedical-jcr-subjects/v1'
export JCR_METRIC_YEAR='2025'
export VENUE_POLICY_NAME='journal-all-q1'
export VENUE_POLICY_REVISION='2'
export VENUE_POLICY_VERSION='journal-all-q1/v2'
export ELIGIBILITY_POLICY_VERSION='biomedical-public-eligibility/v1'
export FORMULA_VERSION='public-catalog/biomedical-v1'
export CITATION_SOURCE='openalex'
export CITATION_FORMULA_VERSION='citation-intelligence/v1'
export CITATION_VELOCITY_WINDOW_DAYS='30'
export CITATION_MINIMUM_COHORT_SIZE='20'
export TREND_RECENT_WINDOW_DAYS='7'
export TREND_BASELINE_WINDOW_DAYS='364'
export TREND_MINIMUM_PAPER_COUNT='20'
export TREND_MINIMUM_INDEPENDENT_JOURNAL_COUNT='3'
export TREND_MINIMUM_INDEPENDENT_TEAM_COUNT='3'
export TREND_MODEL_SELECTION='predeclared_dispersion_threshold'
export TREND_DISPERSION_THRESHOLD='1.5'
export TREND_FORMULA_VERSION='biomedical-trends/v1'
export JOURNAL_WINDOW_DAYS='364'
export JOURNAL_MINIMUM_SUPPORT_COUNT='10'
export JOURNAL_MINIMUM_FIELD_BASELINE_COUNT='40'
export JOURNAL_FORMULA_VERSION='biomedical-journal-patterns/v1'
export OPPORTUNITY_RULE_SET_VERSION='biomedical-opportunity-rules/v1'
export OPPORTUNITY_FORMULA_VERSION='biomedical-opportunities/v1'

export VENUE_ASSESSED_AT='2026-07-17T12:00:00.000000000Z'
export ELIGIBILITY_ASSESSED_AT='2026-07-17T12:05:00.000000000Z'
export ANALYSIS_AS_OF='2026-07-17T12:10:00.000000000Z'
export GENERATED_AT='2026-07-17T12:15:00.000000000Z'
```

Citation、trend、journal 和 opportunity 四个分析步骤必须使用同一个 `ANALYSIS_AS_OF`。Opportunity 会严格校验三个上游 run 的 `as_of`、Subject、eligibility policy、JCR receipt/year、Venue policy 和当前 cohort revision；任一边界不同都会失败。

### 1. 启动 PostgreSQL 并执行迁移

```bash
docker compose up -d --build postgres migrate
docker compose wait migrate
```

`migrate` 的唯一有效调用是：

```text
paper-hub-migrate up
```

它必须成功退出后才能运行任何 Worker Job。API 和 Worker 都不会代替该步骤。

### 2. 导入版本化 biomedical Subject CSV

Core 镜像不包含仓库根目录的 `data/`。必须把 reviewed registry 以只读 bind mount 显式提供给一次性 Worker：

```bash
export SUBJECT_HOST_PATH="$PWD/data/subjects/biomedical-jcr-subjects.v1.csv"

docker compose run --rm \
  --volume "${SUBJECT_HOST_PATH}:/imports/biomedical-jcr-subjects.v1.csv:ro" \
  worker /app/paper-hub-worker import subjects \
  --file /imports/biomedical-jcr-subjects.v1.csv
```

CSV header、大小写和列顺序必须精确为：

```text
source,registry_version,slug,display_label,jcr_category
```

同一文件可幂等重放；同一 source/version 的不同 bytes 会被拒绝。成功结果包含 `file_sha256`、`registry_version`、`subject_count` 和 `rule_count`。

### 3. PubMed 抓取、规范化与投影

当前只有在线 E-utilities 同步命令：

```bash
export NCBI_TOOL='paper-hub'
export NCBI_EMAIL='research@example.com'
# export NCBI_API_KEY='optional-authorized-key'

export PUBMED_QUERY='cancer[Title/Abstract]'
export PUBMED_FROM='2026-07-01'
export PUBMED_TO='2026-07-17'
export PUBMED_MAX_RESULTS='100'

docker compose run --rm \
  --env NCBI_TOOL="${NCBI_TOOL}" \
  --env NCBI_EMAIL="${NCBI_EMAIL}" \
  worker /app/paper-hub-worker sync pubmed \
  --query "${PUBMED_QUERY}" \
  --from-date "${PUBMED_FROM}" \
  --to-date "${PUBMED_TO}" \
  --max-results "${PUBMED_MAX_RESULTS}"
```

如使用 `NCBI_API_KEY`，再向 `docker compose run` 显式增加：

```text
--env NCBI_API_KEY="${NCBI_API_KEY}"
```

`sync pubmed` 在同一个 ingestion job 中依次执行：

```text
Search/Fetch -> PersistRaw -> Normalize -> scope policy -> Project
```

成功 JSON 的关键字段是 `raw_inserted`、`raw_reused`、`projected`、`excluded`、`unchanged` 和 `failed`。JCR importer 只按 ISSN-L、print ISSN、eISSN 解析已经存在的 Venue，因此 PubMed 投影必须先于 JCR 导入完成。

当前**没有实现**：

- `import pubmed-baseline`；
- `import pubmed-daily`；
- 独立 `normalize pubmed`；
- 独立 `project pubmed`。

预留的 `RolePubMedImport` 或环境配置不能视为可调用 Worker 命令。

### 4. 导入用户授权的 JCR CSV

```bash
export JCR_HOST_PATH='/absolute/path/to/authorized-jcr-export.csv'
export JCR_SOURCE_LICENSE='organization-authorized-jcr-export'

docker compose run --rm \
  --volume "${JCR_HOST_PATH}:/imports/jcr.csv:ro" \
  --env JCR_IMPORT_PATH=/imports/jcr.csv \
  --env JCR_SOURCE_LICENSE="${JCR_SOURCE_LICENSE}" \
  worker /app/paper-hub-worker import jcr \
  --file /imports/jcr.csv | tee /tmp/medpaperhub-jcr-import.json
```

强约束：

- `JCR_HOST_PATH` 必须是宿主机绝对路径。
- 容器内 `--file` 必须与 `JCR_IMPORT_PATH` 字节级一致。
- 文件扩展名必须是 `.csv`，不能带 query 或 fragment。
- `JCR_SOURCE_LICENSE` 不能为空。
- 每行必须通过 exact ISSN-L、print ISSN 或 eISSN 命中且只命中一个现有 Venue；title 只能作为 alias evidence，不能解析 Venue。
- 任一行无匹配、多匹配或冲突时，导入失败，不允许 title similarity fallback。

Worker 结果当前不输出 receipt UUID，只输出 `file_sha256` 等摘要。把结果中的 64 位 `file_sha256` 复制到变量，再从 PostgreSQL 精确查询：

```bash
export JCR_FILE_SHA256='<file_sha256 from /tmp/medpaperhub-jcr-import.json>'

export JCR_RECEIPT="$(
  docker compose exec -T postgres psql \
    -U "${POSTGRES_USER:-paper_hub}" \
    -d "${POSTGRES_DB:-paper_hub}" \
    -At -v ON_ERROR_STOP=1 \
    -v file_sha256="${JCR_FILE_SHA256}" \
    -c "SELECT id::text FROM jcr_import_receipts WHERE file_sha256 = :'file_sha256';"
)"

test -n "${JCR_RECEIPT}"
printf 'JCR_RECEIPT=%s\n' "${JCR_RECEIPT}"
```

### 5. JCR exact-link reconciliation

当前没有独立的 `reconcile` CLI。`import subjects` 和 `import jcr` 都会在各自 PostgreSQL 事务提交前调用同一个幂等 reconciliation：

```text
venue_metric_snapshots.category
  == biomedical_subject_rules.jcr_category
  -> journal_subject_metrics
```

匹配是字节精确等值，不 trim、不大小写折叠、不 alias、不前缀、不子串匹配。两个 importer 都会执行它，因此 Subject/JCR 的导入先后在实现上可交换；本部署流程仍固定 Subject 在前，以便审计时先声明范围。

验证当前 JCR receipt 与 Subject version 至少产生一个 exact link：

```bash
docker compose exec -T postgres psql \
  -U "${POSTGRES_USER:-paper_hub}" \
  -d "${POSTGRES_DB:-paper_hub}" \
  -v ON_ERROR_STOP=1 \
  -v receipt="${JCR_RECEIPT}" \
  -v subject_version="${SUBJECT_VERSION}" <<'SQL'
SELECT count(*) AS exact_subject_links
FROM jcr_import_receipt_metrics AS receipt_metric
JOIN venue_metric_snapshots AS metric
  ON metric.id = receipt_metric.metric_snapshot_id
JOIN journal_subject_metrics AS link
  ON link.venue_metric_snapshot_id = metric.id
JOIN biomedical_subject_rules AS rule
  ON rule.id = link.subject_rule_id
JOIN subject_versions AS version
  ON version.id = rule.subject_version_id
WHERE receipt_metric.import_receipt_id = :'receipt'::uuid
  AND version.version_key = :'subject_version'
  AND link.jcr_category = metric.category
  AND rule.jcr_category = metric.category;
SQL
```

`exact_subject_links = 0` 不是可发布的 biomedical 数据状态；不要添加近似匹配补救。

### 6. Venue assessment

```bash
docker compose run --rm \
  worker /app/paper-hub-worker assess venues \
  --metric-year "${JCR_METRIC_YEAR}" \
  --policy-version "${VENUE_POLICY_VERSION}" \
  --assessed-at "${VENUE_ASSESSED_AT}" \
  --jcr-receipt "${JCR_RECEIPT}"
```

唯一允许的完整 policy version/label 是 `journal-all-q1/v2`。结果按 `accepted`、`rejected`、`unknown`、`not_applicable` 汇总：

- `accepted`：至少一个授权 exact JCR Category row 为 Q1；JIF、JIF percentile 和 rank 不会单独触发接受；
- `unknown`：缺少该年指标或存在 unknown metric，且没有命中门槛；
- `rejected`：有完整 known evidence，但没有授权 JCR Q1 证据；
- `not_applicable`：非 journal Venue。

### 7. Biomedical eligibility batch

```bash
docker compose run --rm \
  worker /app/paper-hub-worker assess biomedical-eligibility \
  --metric-year "${JCR_METRIC_YEAR}" \
  --subject-version "${SUBJECT_VERSION}" \
  --policy-version "${ELIGIBILITY_POLICY_VERSION}" \
  --assessed-at "${ELIGIBILITY_ASSESSED_AT}"
```

唯一允许的 eligibility policy 是 `biomedical-public-eligibility/v1`。该命令枚举当前 Work 并固化 `accepted`、`rejected`、`missing`：

- `accepted` 要求 verified journal Venue 和 exact `journal_subject_metrics` evidence；
- `rejected` 表示存在该 Venue/year 指标，但没有 exact Subject link；
- `missing` 明确记录 Venue 未绑定、非 verified journal 或该年指标缺失等原因。

发布前必须至少有一个 `accepted`，否则 Catalog publisher 会因空公开域拒绝发布。

### 8. 生成严格摘要研究路线 run

远程 OpenAI-compatible base URL 必须使用 HTTPS。将
`OPENAI_BASE_URL`、`OPENAI_API_MODE`、`OPENAI_API_KEY`、`OPENAI_MODEL`
写入被 Git 忽略的 `deploy/env/.env.pipeline`；根 Compose 不显式覆盖这些
变量，Worker overlay 按文件提供给一次性任务。`OPENAI_API_MODE` 只能是
`responses` 或 `chat_completions`，必须根据已验证的网关能力显式冻结；
系统不会自动探测、降级或切换接口。

```bash
docker compose \
  -f docker-compose.yml \
  -f deploy/compose/compose.local.yml \
  --profile jobs run --rm \
  worker /app/paper-hub-worker analyze abstract-routes \
  --prompt-version abstract-route-prompt/v1 \
  --schema-version abstract-route/v1 \
  --analysis-cutoff "${ABSTRACT_ANALYSIS_CUTOFF}" \
  --limit "${ABSTRACT_ANALYSIS_LIMIT}"
```

命令只选择 `analysis-cutoff` 之前、具有 `normalized-record/v4` 标题和摘要的精确
Work revision。每个 run 保存：

- `api_mode`，即本次使用的显式 OpenAI-compatible 接口模式；
- `requested_model` 与 provider 响应中的 `actual_model`；
- 输入标题、输入摘要及各自 SHA-256；
-完整 prompt、Schema JSON、版本及 SHA-256；
- 响应 ID、token usage、状态和稳定 failure code；
- 通过 `abstract-route/v1` 的结构化输出与每个字段的摘要证据。

证据必须在规范化输入摘要中出现；缺失、伪造、拒答、截断或 Schema 不匹配都会使该
run 失败，不会转成自由文本或调用第二供应商。Worker 崩溃留下的 `running` run 使用
固定 lease 回收为失败记录，后续命令可以重新分析同一 revision。

```sql
SELECT
    api_mode,
    requested_model,
    actual_model,
    prompt_version,
    schema_version,
    input_sha256,
    schema_sha256,
    response_id,
    status
FROM abstract_route_analysis_runs
ORDER BY started_at DESC, id DESC
LIMIT 20;
```

### 9. 生成不可变引用分析 run

引用来源必须显式选择，不能把 OpenAlex、Crossref 或其他来源的计数混在同一个速度与百分位结果中：

```bash
docker compose run --rm \
  worker /app/paper-hub-worker analyze citations \
  --as-of "${ANALYSIS_AS_OF}" \
  --source "${CITATION_SOURCE}" \
  --velocity-window-days "${CITATION_VELOCITY_WINDOW_DAYS}" \
  --minimum-cohort-size "${CITATION_MINIMUM_COHORT_SIZE}" \
  --formula-version "${CITATION_FORMULA_VERSION}" \
  --subject-version "${SUBJECT_VERSION}" \
  --eligibility-policy-version "${ELIGIBILITY_POLICY_VERSION}" \
  --jcr-metric-year "${JCR_METRIC_YEAR}" \
  --jcr-import-receipt "${JCR_RECEIPT}" \
  | tee /tmp/medpaperhub-citation-analysis.json
```

成功结果包含 `analysis_run_id`、known/insufficient velocity 数量和 cohort percentile 数量。提取本次 run：

```bash
export CITATION_ANALYSIS_RUN_ID="$(
  python3 -c 'import json,sys,uuid; print(uuid.UUID(json.load(sys.stdin)["analysis_run_id"]))' \
    < /tmp/medpaperhub-citation-analysis.json
)"

test -n "${CITATION_ANALYSIS_RUN_ID}"
printf 'CITATION_ANALYSIS_RUN_ID=%s\n' "${CITATION_ANALYSIS_RUN_ID}"
```

该 run 必须与后续 Catalog 的 citation source、JCR receipt/year、Subject version、eligibility policy 和 Venue policy 完全匹配，并且在 `GENERATED_AT` 之前成功完成。缺少同源边界快照时，velocity 会持久化为 `insufficient_evidence`，不会填 `0`。

### 10. 生成不可变 publication trend analysis run

```bash
docker compose run --rm \
  worker /app/paper-hub-worker analyze trends \
  --recent-window-days "${TREND_RECENT_WINDOW_DAYS}" \
  --baseline-window-days "${TREND_BASELINE_WINDOW_DAYS}" \
  --minimum-paper-count "${TREND_MINIMUM_PAPER_COUNT}" \
  --minimum-independent-journal-count "${TREND_MINIMUM_INDEPENDENT_JOURNAL_COUNT}" \
  --minimum-independent-team-count "${TREND_MINIMUM_INDEPENDENT_TEAM_COUNT}" \
  --model-selection "${TREND_MODEL_SELECTION}" \
  --dispersion-threshold "${TREND_DISPERSION_THRESHOLD}" \
  --formula-version "${TREND_FORMULA_VERSION}" \
  --as-of "${ANALYSIS_AS_OF}" \
  --subject-version "${SUBJECT_VERSION}" \
  --eligibility-policy-version "${ELIGIBILITY_POLICY_VERSION}" \
  --jcr-metric-year "${JCR_METRIC_YEAR}" \
  --jcr-import-receipt "${JCR_RECEIPT}" \
  --venue-policy-name "${VENUE_POLICY_NAME}" \
  --venue-policy-version "${VENUE_POLICY_REVISION}" \
  | tee /tmp/medpaperhub-trend-analysis.json
```

`TREND_RECENT_WINDOW_DAYS` 必须为 `7`，与当前 Catalog Home 的 7 天窗口一致；其他正数虽然可能通过 CLI 解析，但 Publisher 会拒绝不兼容的 trend run。成功结果包含 `analysis_run_id`、`snapshot_count` 和 `cohort_revisions`。提取本次 run：

```bash
export TREND_ANALYSIS_RUN_ID="$(
  python3 -c 'import json,sys,uuid; print(uuid.UUID(json.load(sys.stdin)["analysis_run_id"]))' \
    < /tmp/medpaperhub-trend-analysis.json
)"

test -n "${TREND_ANALYSIS_RUN_ID}"
printf 'TREND_ANALYSIS_RUN_ID=%s\n' "${TREND_ANALYSIS_RUN_ID}"
```

### 11. 生成不可变 journal editorial-pattern analysis run

```bash
docker compose run --rm \
  worker /app/paper-hub-worker analyze journals \
  --window-days "${JOURNAL_WINDOW_DAYS}" \
  --minimum-support-count "${JOURNAL_MINIMUM_SUPPORT_COUNT}" \
  --minimum-field-baseline-count "${JOURNAL_MINIMUM_FIELD_BASELINE_COUNT}" \
  --formula-version "${JOURNAL_FORMULA_VERSION}" \
  --as-of "${ANALYSIS_AS_OF}" \
  --subject-version "${SUBJECT_VERSION}" \
  --eligibility-policy-version "${ELIGIBILITY_POLICY_VERSION}" \
  --jcr-metric-year "${JCR_METRIC_YEAR}" \
  --jcr-import-receipt "${JCR_RECEIPT}" \
  --venue-policy-name "${VENUE_POLICY_NAME}" \
  --venue-policy-version "${VENUE_POLICY_REVISION}" \
  | tee /tmp/medpaperhub-journal-analysis.json
```

成功结果包含 `analysis_run_id`、`snapshot_count` 和 `cohort_revisions`。提取本次 run：

```bash
export JOURNAL_ANALYSIS_RUN_ID="$(
  python3 -c 'import json,sys,uuid; print(uuid.UUID(json.load(sys.stdin)["analysis_run_id"]))' \
    < /tmp/medpaperhub-journal-analysis.json
)"

test -n "${JOURNAL_ANALYSIS_RUN_ID}"
printf 'JOURNAL_ANALYSIS_RUN_ID=%s\n' "${JOURNAL_ANALYSIS_RUN_ID}"
```

### 12. 生成不可变 research opportunity analysis run

Opportunity 必须显式绑定本批次的 citation、trend 和 journal 三个 succeeded run：

```bash
docker compose run --rm \
  worker /app/paper-hub-worker analyze opportunities \
  --rule-set-version "${OPPORTUNITY_RULE_SET_VERSION}" \
  --formula-version "${OPPORTUNITY_FORMULA_VERSION}" \
  --citation-analysis-run-id "${CITATION_ANALYSIS_RUN_ID}" \
  --trend-analysis-run-id "${TREND_ANALYSIS_RUN_ID}" \
  --journal-analysis-run-id "${JOURNAL_ANALYSIS_RUN_ID}" \
  --as-of "${ANALYSIS_AS_OF}" \
  --subject-version "${SUBJECT_VERSION}" \
  --eligibility-policy-version "${ELIGIBILITY_POLICY_VERSION}" \
  --jcr-metric-year "${JCR_METRIC_YEAR}" \
  --jcr-import-receipt "${JCR_RECEIPT}" \
  --venue-policy-name "${VENUE_POLICY_NAME}" \
  --venue-policy-version "${VENUE_POLICY_REVISION}" \
  | tee /tmp/medpaperhub-opportunity-analysis.json
```

成功结果包含 `analysis_run_id`、`snapshot_count` 和 `cohort_revisions`。提取本次 run：

```bash
export OPPORTUNITY_ANALYSIS_RUN_ID="$(
  python3 -c 'import json,sys,uuid; print(uuid.UUID(json.load(sys.stdin)["analysis_run_id"]))' \
    < /tmp/medpaperhub-opportunity-analysis.json
)"

test -n "${OPPORTUNITY_ANALYSIS_RUN_ID}"
printf 'OPPORTUNITY_ANALYSIS_RUN_ID=%s\n' "${OPPORTUNITY_ANALYSIS_RUN_ID}"
```

### 13. 发布 immutable Catalog generation

```bash
docker compose run --rm \
  worker /app/paper-hub-worker publish catalog \
  --formula-version "${FORMULA_VERSION}" \
  --generated-at "${GENERATED_AT}" \
  --jcr-metric-year "${JCR_METRIC_YEAR}" \
  --venue-policy-name "${VENUE_POLICY_NAME}" \
  --venue-policy-version "${VENUE_POLICY_REVISION}" \
  --eligibility-policy-version "${ELIGIBILITY_POLICY_VERSION}" \
  --subject-version "${SUBJECT_VERSION}" \
  --jcr-import-receipt "${JCR_RECEIPT}" \
  --citation-source "${CITATION_SOURCE}" \
  --citation-analysis-run-id "${CITATION_ANALYSIS_RUN_ID}" \
  --trend-analysis-run-id "${TREND_ANALYSIS_RUN_ID}" \
  --journal-analysis-run-id "${JOURNAL_ANALYSIS_RUN_ID}" \
  --opportunity-analysis-run-id "${OPPORTUNITY_ANALYSIS_RUN_ID}" \
  | tee /tmp/medpaperhub-catalog-publish.json
```

所有参数都是必填。Publisher 对 Venue policy 执行严格身份校验：`--venue-policy-name` 接收 policy name `journal-all-q1`，`--venue-policy-version` 接收 policy revision `2`，两者共同对应完整 policy version/label `journal-all-q1/v2`。Publisher 只纳入同时满足以下条件的 Work：

- 当前 source state 为 `included`，且 raw/normalization/projection/Work provenance 完整；
- Work 为 `active`；
- Venue 是带 controlled ISSN 的 journal；
- 指定 JCR receipt/year/policy 的 Venue assessment 为 `accepted`；
- 指定 Subject version/year/policy 的 biomedical eligibility 为 `accepted`；
- eligibility 的 decisive exact Subject link 属于同一 JCR receipt。
- 指定 citation analysis run 已成功完成，且其来源、时间与全部 biomedical/JCR 范围参数和本次发布完全一致。
- 指定 trend、journal 和 opportunity analysis run 均已成功完成、属于当前 cohort、在 `GENERATED_AT` 前可用，并与本次发布的 biomedical/JCR/Venue policy 范围完全一致；
- opportunity run 显式绑定本次发布选择的 citation、trend 和 journal 三个上游 run ID。

成功 stdout 包含 generation ID、source revision、formula version、生成/发布时间以及全部 curation references。验证发布结果确实回显同一组四个 analysis run ID：

```bash
python3 <<'PY'
import json
import os
import uuid

with open("/tmp/medpaperhub-catalog-publish.json", encoding="utf-8") as stream:
    result = json.load(stream)

for field, environment_name in (
    ("citation_analysis_run_id", "CITATION_ANALYSIS_RUN_ID"),
    ("trend_analysis_run_id", "TREND_ANALYSIS_RUN_ID"),
    ("journal_analysis_run_id", "JOURNAL_ANALYSIS_RUN_ID"),
    ("opportunity_analysis_run_id", "OPPORTUNITY_ANALYSIS_RUN_ID"),
):
    actual = uuid.UUID(result[field])
    expected = uuid.UUID(os.environ[environment_name])
    if actual != expected:
        raise SystemExit(f"{field} mismatch: {actual} != {expected}")

print(f"CATALOG_GENERATION_ID={uuid.UUID(result['id'])}")
PY
```

相同 source revision 会复用 immutable generation。

### 13. 启动 API 与 Nuxt Web

```bash
docker compose up -d api web
```

验证：

```bash
curl -i http://localhost:8080/health
curl -i http://localhost:3000/health
curl -i http://localhost:8080/api/v1/home
curl -i http://localhost:8080/api/v1/subjects
curl -i http://localhost:8080/api/v1/journals
```

已发布响应必须包含 `X-Request-ID` 与 `X-Catalog-Generation`。

## 没有授权 JCR 时的公开行为

在空库或新环境中，如果没有用户授权 JCR CSV：

1. 只执行迁移和允许的数据源同步；
2. 不运行 `import jcr`、Venue assessment、biomedical eligibility、四类 analysis 或 Catalog publish；
3. 绝不挂载 `data/venues/jcr-q1.example.csv`；
4. 可以启动 API/Web 暴露明确的未发布状态。

真实 API 路由是：

```text
/api/v1/home
/api/v1/subjects
/api/v1/journals
```

它们在没有已发布 generation 时必须返回：

```text
HTTP 503
code = catalog_not_published
```

Nuxt 对应页面是 `/`、`/subjects`、`/journals`；它们消费上述 503 并呈现等待首次发布状态，不返回 fixture collection 或伪造 `200` Catalog API 数据。

## Docker volume 规则

本地 Compose 的 PostgreSQL 持久卷是：

```text
postgres_data:/var/lib/postgresql
```

CSV 使用单次、只读 bind mount：

```text
Subject: <absolute-host-path>:/imports/biomedical-jcr-subjects.v1.csv:ro
JCR:     <absolute-host-path>:/imports/jcr.csv:ro
```

规则：

- 不把授权 JCR CSV `COPY` 进镜像；
- 不把 CSV 挂载给 API 或 Web；
- 不挂载整个宿主目录，只挂载本次导入的单个文件；
- Worker 退出后不保留 CSV 容器；
- receipt、SHA-256、source/license 和 metric links 保存在 PostgreSQL。

## 本地原生运行

可只用 Docker 启动 PostgreSQL，其余进程在宿主机运行。

### PostgreSQL 与迁移

```bash
docker compose up -d postgres

export DATABASE_URL='postgres://paper_hub:paper_hub_local@localhost:5432/paper_hub?sslmode=disable'
go -C services/core run ./cmd/migrate up
```

### Subject、PubMed 与 JCR

```bash
export REPOSITORY_ROOT="$PWD"

go -C services/core run ./cmd/worker import subjects \
  --file "${REPOSITORY_ROOT}/data/subjects/biomedical-jcr-subjects.v1.csv"

export NCBI_TOOL='paper-hub'
export NCBI_EMAIL='research@example.com'

go -C services/core run ./cmd/worker sync pubmed \
  --query 'cancer[Title/Abstract]' \
  --from-date 2026-07-01 \
  --to-date 2026-07-17 \
  --max-results 100

export JCR_IMPORT_PATH='/absolute/path/to/authorized-jcr-export.csv'
export JCR_SOURCE_LICENSE='organization-authorized-jcr-export'

go -C services/core run ./cmd/worker import jcr \
  --file "${JCR_IMPORT_PATH}"
```

后续 assessment/analyze/publish 使用与 Docker 章节相同的 flags，把命令前缀替换为：

```text
go -C services/core run ./cmd/worker
```

本地 receipt 查询可直接使用：

```bash
psql "${DATABASE_URL}" -At \
  -v file_sha256="${JCR_FILE_SHA256}" \
  -c "SELECT id::text FROM jcr_import_receipts WHERE file_sha256 = :'file_sha256';"
```

### API 与 Web

终端一：

```bash
export DATABASE_URL='postgres://paper_hub:paper_hub_local@localhost:5432/paper_hub?sslmode=disable'
export CATALOG_CURSOR_SECRET='development-only-catalog-cursor-secret-change-me'
go -C services/core run ./cmd/api
```

终端二：

```bash
export INTERNAL_API_BASE_URL='http://localhost:8080'
export NUXT_PUBLIC_API_BASE_URL='http://localhost:8080'
pnpm --dir apps/web dev
```

生产 Nuxt bundle：

```bash
pnpm --dir apps/web build

HOST=0.0.0.0 \
PORT=3000 \
NITRO_HOST=0.0.0.0 \
NITRO_PORT=3000 \
INTERNAL_API_BASE_URL='http://api:8080' \
NUXT_PUBLIC_API_BASE_URL='https://api.example.com' \
node apps/web/.output/server/index.mjs
```

`NUXT_PUBLIC_API_BASE_URL` 是浏览器可解析的外部 API origin；`INTERNAL_API_BASE_URL` 只用于 Nuxt SSR 到私网 Go API。

## 当前 Go 命令与 flags

Worker 顶层形式：

```text
paper-hub-worker <sync|import|assess|analyze|publish> <source> [flags]
```

`--max-results` 在全部 sync 命令中都是必填，范围 `1..1000`。

| 命令 | 当前 flags | 解析约束 |
| --- | --- | --- |
| `sync openalex` | `--query string`、`--filter string`、`--max-results int` | query/filter 至少一个；max-results 1..1000 |
| `sync pubmed` | `--query string`、`--from-date YYYY-MM-DD`、`--to-date YYYY-MM-DD`、repeatable `--issn string`、`--max-results int` | 日期两者都必填且 from <= to；query/ISSN 至少一个；max-results 1..1000 |
| `sync crossref-created` | `--from-date YYYY-MM-DD`、`--to-date YYYY-MM-DD`、repeatable `--issn string`、`--max-results int` | date pair 必填；使用 Crossref `created` 流做首次发现；max-results 1..1000 |
| `sync crossref-updated` | `--from-date YYYY-MM-DD`、`--to-date YYYY-MM-DD`、repeatable `--issn string`、`--max-results int` | date pair 必填；使用 Crossref `update/deposited` 流做修订同步；`indexed` 不推进发现时间；max-results 1..1000 |
| `import subjects` | `--file path.csv` | 必填；trim 后非空；显式 `.csv`；不能含 `?`/`#` |
| `import jcr` | `--file path.csv` | 同上；运行时还要求与 `JCR_IMPORT_PATH` 完全一致，并要求 `JCR_SOURCE_LICENSE` |
| `assess venues` | `--metric-year int`、`--policy-version string`、`--assessed-at RFC3339Nano`、`--jcr-receipt UUID` | year 1900..3000；完整 policy version/label 必须为 `journal-all-q1/v2`；时间非零；receipt 必填 |
| `assess biomedical-eligibility` | `--metric-year int`、`--subject-version string`、`--policy-version string`、`--assessed-at RFC3339Nano` | year 1900..3000；Subject version 必填；policy 必须为 `biomedical-public-eligibility/v1`；时间非零 |
| `analyze abstract-routes` | `--prompt-version string`、`--schema-version string`、`--analysis-cutoff RFC3339Nano`、`--limit int` | 全部必填；prompt 固定为 `abstract-route-prompt/v1`；Schema 固定为 `abstract-route/v1`；limit 1..1000；运行角色还必须提供 `OPENAI_BASE_URL`、显式 `OPENAI_API_MODE=responses\|chat_completions`、`OPENAI_API_KEY`、`OPENAI_MODEL` |
| `analyze citations` | `--as-of RFC3339Nano`、`--source string`、`--velocity-window-days int`、`--minimum-cohort-size int`、`--formula-version string`、`--subject-version string`、`--eligibility-policy-version string`、`--jcr-metric-year int`、`--jcr-import-receipt UUID` | 全部必填；窗口 1..3650；cohort 2..1000000；formula 固定为 `citation-intelligence/v1`；不混合 citation source |
| `analyze trends` | `--recent-window-days int`、`--baseline-window-days int`、`--minimum-paper-count int`、`--minimum-independent-journal-count int`、`--minimum-independent-team-count int`、`--model-selection string`、`--dispersion-threshold float`、`--formula-version string`，以及 `--as-of`、Subject/eligibility/JCR/Venue policy scope flags | 全部必填；baseline 必须大于 recent；formula 固定为 `biomedical-trends/v1`；用于 Catalog publish 的 recent window 必须为 7；Venue policy name=`journal-all-q1`，revision=`2` |
| `analyze journals` | `--window-days int`、`--minimum-support-count int`、`--minimum-field-baseline-count int`、`--formula-version string`，以及 `--as-of`、Subject/eligibility/JCR/Venue policy scope flags | 全部必填；field baseline 不小于 support；formula 固定为 `biomedical-journal-patterns/v1`；Venue policy name=`journal-all-q1`，revision=`2` |
| `analyze opportunities` | `--rule-set-version string`、`--formula-version string`、`--citation-analysis-run-id UUID`、`--trend-analysis-run-id UUID`、`--journal-analysis-run-id UUID`，以及 `--as-of`、Subject/eligibility/JCR/Venue policy scope flags | 全部必填；必须同时绑定三个 succeeded 上游 run；rule set 固定为 `biomedical-opportunity-rules/v1`；formula 固定为 `biomedical-opportunities/v1`；上游 scope 必须与本命令完全一致 |
| `publish catalog` | `--formula-version string`、`--generated-at RFC3339Nano`、`--jcr-metric-year int`、`--venue-policy-name string`、`--venue-policy-version int`、`--eligibility-policy-version string`、`--subject-version string`、`--jcr-import-receipt UUID`、`--citation-source string`、`--citation-analysis-run-id UUID`、`--trend-analysis-run-id UUID`、`--journal-analysis-run-id UUID`、`--opportunity-analysis-run-id UUID` | 全部必填；year 1900..3000；policy name 必须为 `journal-all-q1`，`--venue-policy-version` 必须传 policy revision `2`；四个 analysis run 必须是匹配范围的 succeeded deterministic run |

其他二进制：

| 二进制 | CLI |
| --- | --- |
| `paper-hub-migrate` | 仅 `up` |
| `paper-hub-api` | 没有 CLI flags；全部配置来自环境变量 |

当前 Worker 的 `flag.FlagSet` 丢弃详细 usage 输出，因此 `... --help` 用于确认命令路由时会以 code 2 返回 `flag: help requested`，不会打印完整 flag 表。生产调度应以本节和 parse tests 为准，不解析 help 文本生成参数。

## 明确未实现

以下名称不能进入部署清单或 scheduler：

- PubMed Baseline 文件导入；
- PubMed Daily Update 文件导入；
- 独立 PubMed normalize；
- 独立 PubMed project；
- 独立 JCR/Subject reconciliation；
- PMC OA 全文同步；
- Springer Nature 同步；
- Elsevier 同步。

`.env.example` 中存在预留配置不表示对应 Worker 命令已经注册。
