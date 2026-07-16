# Paper API Quality Hardening Implementation Plan

> **For Codex:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development and execute each task RED → GREEN.

**Goal:** 修复 scope/投影一致性、排名统计口径、查询一致性与性能、API 契约中的全部质量审查项。

**Architecture:** 使用 PostgreSQL 0010 migration 将 Work 投影锚定到精确 SourceRecord UUID，并为 excluded assessment 的 null→Work 关联增加审计字段和数据库触发器；同一指标时间点以精确 SourceRecord provenance 确定修订赢家。搜索在只读 REPEATABLE READ 事务中返回 `snapshot_at + snapshot_revision`：新建 Work 继续由 `created_at` 边界排除，既有 Work 的可搜索状态若发生变化则由 0011 change log 明确使续页失效并返回 409，避免 offset 静默重复或漏项。facets 使用单条 MATERIALIZED CTE 聚合。排名使用数据库边界快照、窗口函数和聚合完成候选选择、排序及 limit，只把有界结果返回 Python。

**Tech Stack:** Python 3.12、FastAPI、Pydantic v2、SQLAlchemy 2、Alembic、PostgreSQL 16/pgvector、pytest、Next.js/pnpm。

---

### Task 1: Scope 乱序关联与精确投影

**Files:**
- Create: `services/api/alembic/versions/0010_projection_search_ranking_integrity.py`
- Modify: `services/api/src/paper_hub/models.py`
- Modify: `services/api/src/paper_hub/ingestion.py`
- Test: `services/api/tests/test_ingestion.py`
- Test: `services/api/tests/test_postgres_integrity.py`

1. 写真实 PG RED：新 excluded snapshot 先到、旧 included snapshot 后到，最新 excluded 必须隐藏 Work；关联只能 null→已确定 Work，并留下 linked_at/link_reason。
2. 写 RED：相同 source_updated_at、不同 retrieved_at/id 时，较新 snapshot 成为 projection；旧 assertion 不得命中 item/filter。
3. 写 0010 migration：ScopeAssessment 审计列、逻辑来源回填 trigger、Work 精确 projection SourceRecord FK、稳定回填及一致性 trigger。
4. 调整 ingestion：先持久化/绑定 SourceRecord，再按 `(source_updated_at NULLS LAST, retrieved_at, id)` 更新投影。
5. 定向运行 ingestion/integrity 测试至 GREEN，并验证 0009↔0010 往返。

### Task 2: Scope detail 契约

**Files:**
- Modify: `services/api/src/paper_hub/config.py`
- Modify: `services/api/src/paper_hub/repositories.py`
- Modify: `services/api/src/paper_hub/api/papers.py`
- Test: `services/api/tests/test_papers_api.py`

1. 写 RED：多来源一排一收时顶层 rule_version 固定配置值，evaluated_at 为各 logical source 当前 decision 最大值。
2. 传入当前配置 scope rule；保留 evidence 中每来源实际 rule_version。
3. 定向 API 测试至 GREEN。

### Task 3: 排名边界快照与 Topic percentile

**Files:**
- Modify: `services/api/src/paper_hub/rankings.py`
- Modify: `services/api/src/paper_hub/repositories.py`
- Modify: `services/api/src/paper_hub/schemas.py`
- Test: `services/api/tests/test_rankings.py`
- Test: `services/api/tests/test_trends_api.py`

1. 写 RED：citation/code 必须选择 baseline `<= window_start`、current `<= now`，缺 baseline 或跨度不足为 missing。
2. 写 RED：citation 按 `(topic, publication_month, type)` 求 percentile，Work 多 Topic 取中位数并返回每 Topic 明细；无 Topic missing。
3. 在 PostgreSQL CTE/window/aggregate 中计算边界点、velocity、cohort percentile、排序和 limit。
4. 写 RED：code 多仓库部分缺失必须逐仓库进入 missing_signals，item 标记 partial coverage。
5. 定向 ranking/trends 测试至 GREEN。

### Task 4: Latest 与 method adoption

**Files:**
- Modify: `services/api/src/paper_hub/repositories.py`
- Modify: `services/api/src/paper_hub/rankings.py`
- Modify: `services/api/src/paper_hub/schemas.py`
- Test: `services/api/tests/test_trends_api.py`

1. 写 RED：latest effective time 为 publication、version update/submission/publication、projection update 的最大值，按精确 duration 入窗。
2. 写 RED：method denominator 为方法关联 Topic 在对应窗口内的 public Work 并集，confidence 使用该 denominator。
3. SQL 下推 latest 排序/limit；taxonomy coverage 保留 limit 前 ranked_subjects。
4. 定向测试至 GREEN。

### Task 5: 稳定搜索、索引与单查询 facets

**Files:**
- Modify: `services/api/src/paper_hub/normalization.py`
- Modify: `services/api/src/paper_hub/repositories.py`
- Modify: `services/api/src/paper_hub/api/papers.py`
- Modify: `services/api/src/paper_hub/schemas.py`
- Modify: `services/api/src/paper_hub/models.py`
- Test: `services/api/tests/test_papers_api.py`
- Test: `services/api/tests/test_postgres_integrity.py`

1. 写 RED：纯空白 q 返回一致 422；DOI/arXiv/OpenAlex/S2 精确识别。
2. 写 RED：一个 list 响应在 REPEATABLE READ READ ONLY 中读取；并发插入不改变该响应，返回 `snapshot_at + snapshot_revision` 后后续页排除新插入；既有 Work 的排序、过滤、facet 或展示状态变化必须返回 `409 search_snapshot_invalidated`，不得静默重复或漏项。
3. 写 RED：facets 只执行一条 MATERIALIZED CTE 聚合查询。
4. 增加默认 DESC/ASC expression index、canonical/external trigram indexes、repo metric index。
5. 用真实 repository SQL 的 EXPLAIN 验证 exact B-tree、general trigram 和默认排序索引。
6. 定向搜索/迁移测试至 GREEN。

### Task 5A: 审查后并发、指标与分页修订

**Files:**
- Create: `services/api/alembic/versions/0011_search_snapshot_invalidation.py`
- Modify: `services/api/alembic/versions/0010_exact_projection_scope_links.py`
- Modify: `services/api/src/paper_hub/ingestion.py`
- Modify: `services/api/src/paper_hub/models.py`
- Modify: `services/api/src/paper_hub/repositories.py`
- Modify: `services/api/src/paper_hub/api/papers.py`
- Modify: `services/api/src/paper_hub/schemas.py`
- Test: `services/api/tests/test_ingestion.py`
- Test: `services/api/tests/test_papers_api.py`
- Test: `services/api/tests/test_postgres_integrity.py`

1. 写真实 PG RED：两个只携带不相交标识的并发快照解析到同一 Work 时，旧事务晚提交不得回退新投影。
2. 在 identity advisory locks 之后对唯一 Work 执行 `SELECT FOR UPDATE` 并刷新 ORM identity map，再进行 canonical/projection/taxonomy 更新。
3. 写 RED：相同 `source_updated_at` 的后抓取精确投影必须确定性替换同一 citation 时间点的值和 SourceRecord provenance；0010 migration 回填并修正历史赢家，数据库双向约束 owner/source/measured_at。
4. 写 RED：首次分页返回 revision；新 Work 插入仍可续页，既有 Work 变化使续页返回 409；首次快照不得锁相关表造成摄入锁顺序循环。
5. 0011 以事务内 `(transaction_id, work_id)` 去重的 change log 捕获 Work、scope、projection assertions、identifiers、taxonomy、code association 和 work metrics 的可观察变更。

### Task 6: 结构化 coverage 与全量门禁

**Files:**
- Modify: `services/api/src/paper_hub/schemas.py`
- Modify: `services/api/src/paper_hub/rankings.py`
- Test: `services/api/tests/test_trends_api.py`

1. 写 RED：coverage 必含 total_eligible、subjects_with_signal、ranked_subjects、returned_subjects、0..1 signal_coverage、confidence；percentile 限定 0..1。
2. 更新所有 paper/taxonomy ranking response，确保 ranked_subjects 不受 limit。
3. 运行全部真实 PostgreSQL pytest、0010 upgrade/downgrade/upgrade、OpenAPI warnings-as-errors、compile/build、前端 test/lint/build。
4. `git diff --check`、审阅完整 diff，提交并确认工作树干净。
