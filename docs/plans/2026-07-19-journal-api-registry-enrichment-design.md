# 2032 本候选期刊 API 注册表补齐设计

**日期：** 2026-07-19  
**状态：** 用户已批准  
**输入：** 医学、生物学、计算机科学三份微信候选期刊 CSV，共 2032 行  
**输出：** 可审计的期刊身份与 API 能力注册表

## 1. 目标

把只有期刊名称的候选名单补齐为可以驱动增量采集的注册表。每一行至少明确：

- 来源领域与原始顺序；
- 来源期刊名；
- ISSN-L、印刷 ISSN、电子 ISSN；
- Crossref 期刊身份、出版商和 DOI 数量；
- OpenAlex Source ID；
- PubMed 是否存在可查询记录；
- 推荐的主发现源和补充源；
- 匹配方法、证据来源和验证状态。

本阶段不把微信名单当作 Clarivate 官方数据，也不把名称近似结果自动写成已验证身份。

## 2. 非目标

- 不从 IF 推断 ISSN、出版商或 API。
- 不用标题相似度、编辑距离、关键词或人工猜测自动绑定期刊。
- 不把 OpenAlex 当作实时增量主源。
- 不抓取付费全文。
- 不在本阶段导入正式 JCR Registry。
- 不把 `JCR Q1` 自动等同于用户要求的 `JIF Percentile >= 87.5`。

## 3. 数据源

### 3.1 Crossref Journals

下载 Crossref `/journals` 全量目录，使用 cursor 分页。保存原始 JSONL、抓取元数据、
页哈希和最终总哈希。Crossref 提供：

- `title`
- `ISSN`
- `issn-type`
- `publisher`
- `counts.total-dois`
- 当前元数据覆盖字段

### 3.2 OpenAlex Sources

只对已获得合法 ISSN 的记录调用单实体 Source 查询：

```text
/sources/issn:{ISSN}
```

用于补充：

- `issn_l`
- 完整 ISSN 集合
- `openalex_source_id`
- host organization

OpenAlex 失败不会覆盖 Crossref 身份，也不会把记录降级为不存在。

### 3.3 PubMed E-utilities

对每个已解析期刊，把已知 ISSN 合并成同一个精确 OR 查询，使用 `retmax=0` 获取记录数。
记录：

- `pubmed_supported=yes`：返回数量大于 0；
- `pubmed_supported=no`：查询成功且数量为 0；
- `pubmed_supported=unknown`：请求失败、限流或响应不可验证。

不把 PubMed 缺失解释为期刊不存在。

## 4. 确定性匹配

源期刊名和 Crossref 标题只进行以下规范化：

1. Unicode NFKC；
2. Unicode case fold；
3. Unicode 空白折叠为单个 ASCII 空格；
4. 去除首尾空白。

不删除标点，不替换连字符，不展开缩写，不做编辑距离。

匹配状态：

```text
resolved
ambiguous
unresolved
```

规则：

- 规范化标题只对应一个 Crossref ISSN 集合时，进入候选验证；
- 多个 Crossref 条目若最终解析到同一 OpenAlex Source ID 和同一 ISSN-L，可合并为一个身份；
- 多个候选解析到不同 ISSN-L，标记 `ambiguous`；
- 没有唯一证据，标记 `unresolved`；
- `ambiguous` 和 `unresolved` 行不得进入正式 Venue Registry。

## 5. 输出

### 5.1 CSV

```text
data/venues/journal-api-registry.v1.csv
```

一行对应一条输入候选记录，保留原始顺序。主要字段：

```text
domain
source_order
source_journal_name
impact_factor
jcr_value
cass_value
issn_l
print_issn
eissn
all_issns
crossref_title
crossref_publisher
crossref_total_dois
crossref_supported
openalex_source_id
openalex_supported
pubmed_supported
pubmed_record_count
primary_discovery_source
secondary_sources
match_method
match_status
source_url
verification_status
```

### 5.2 机器报告

```text
data/venues/journal-api-registry.v1.report.json
```

记录：

- 输入文件 SHA-256；
- Crossref 目录抓取时间和 SHA-256；
- 每个领域的 resolved / ambiguous / unresolved 数量；
- API 覆盖数量；
- 精确重复与 ISSN 冲突；
- 网络失败和未完成探测；
- 完整输出 SHA-256。

## 6. 采集策略

补齐后，注册表驱动以下周期：

```text
重点期刊 Crossref created：每 15 分钟
其他期刊 Crossref created：每 1 小时
Crossref updated：每 1 小时
PubMed 状态：每 3 小时
bioRxiv / medRxiv / arXiv：每 1 小时，独立于期刊名单
```

每个来源、期刊和 stream 保存独立 watermark。公开“最新”使用明确发表事件日期；
`discovered_at` 只表示本系统首次发现时间。

## 7. 业务边界

当前实现中的 `journal-all-q1/v2` 与用户规则冲突。正式导入前必须新增不可变的新策略版本：

```text
JCR Q1 AND JIF Percentile >= 87.5
```

旧策略版本只能用于历史审计，不能修改其既有语义。

