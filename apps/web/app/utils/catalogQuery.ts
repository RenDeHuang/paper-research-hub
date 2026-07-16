import type {
  OpportunityQuery,
  PageQuery,
  PaperLifecycleStatus,
  PaperListQuery,
  PaperSort,
  PaperType,
  ResearchOpportunityStatus,
  SourceName,
  TrendQuery,
} from "~/types/catalog"

type RouteQueryValue = null | string | Array<null | string> | undefined
type RouteQuery = Record<string, RouteQueryValue>

const paperTypes = new Set<PaperType>([
  "research_article",
  "review",
  "preprint",
  "dataset",
  "benchmark",
])
const lifecycleStatuses = new Set<PaperLifecycleStatus>([
  "active",
  "withdrawn",
  "retracted",
  "rejected",
  "superseded",
])
const sources = new Set<SourceName>([
  "crossref",
  "arxiv",
  "openreview",
  "s2",
  "pubmed",
  "pmc",
  "openalex",
  "springer_nature",
  "elsevier",
  "manual",
])
const paperSorts = new Set<PaperSort>([
  "relevance",
  "published_at_desc",
  "citations_desc",
  "trend_desc",
])
const opportunityStatuses = new Set<ResearchOpportunityStatus>([
  "worth_pursuing",
  "proceed_with_caution",
  "not_recommended_now",
  "insufficient_evidence",
])
const slugPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
const rfc3339Pattern =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/

export class CatalogQueryError extends Error {
  readonly parameter: string

  constructor(parameter: string, message = "查询参数无效") {
    super(`${message}: ${parameter}`)
    this.name = "CatalogQueryError"
    this.parameter = parameter
  }
}

function assertAllowedKeys(query: RouteQuery, allowed: ReadonlySet<string>) {
  for (const key of Object.keys(query)) {
    if (!allowed.has(key)) {
      throw new CatalogQueryError(key, "不支持的查询参数")
    }
  }
}

function singleValue(query: RouteQuery, parameter: string) {
  const value = query[parameter]
  if (value === undefined || value === null) {
    return undefined
  }
  if (Array.isArray(value)) {
    throw new CatalogQueryError(parameter, "查询参数不能重复")
  }
  if (value.length === 0) {
    throw new CatalogQueryError(parameter, "查询参数不能为空")
  }
  return value
}

function optionalInteger(
  query: RouteQuery,
  parameter: string,
  minimum: number,
  maximum: number,
) {
  const value = singleValue(query, parameter)
  if (value === undefined) {
    return undefined
  }
  if (!/^\d+$/.test(value)) {
    throw new CatalogQueryError(parameter, "查询参数必须是十进制整数")
  }
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw new CatalogQueryError(parameter, "查询参数超出允许范围")
  }
  return parsed
}

function optionalBoolean(query: RouteQuery, parameter: string) {
  const value = singleValue(query, parameter)
  if (value === undefined) {
    return undefined
  }
  if (value === "true") {
    return true
  }
  if (value === "false") {
    return false
  }
  throw new CatalogQueryError(parameter, "布尔参数只能是 true 或 false")
}

function optionalEnum<T extends string>(
  query: RouteQuery,
  parameter: string,
  values: ReadonlySet<T>,
) {
  const value = singleValue(query, parameter)
  if (value === undefined) {
    return undefined
  }
  if (!values.has(value as T)) {
    throw new CatalogQueryError(parameter, "查询参数不在允许枚举内")
  }
  return value as T
}

function optionalSlug(query: RouteQuery, parameter: string) {
  const value = singleValue(query, parameter)
  if (value === undefined) {
    return undefined
  }
  if (value.length > 120 || !slugPattern.test(value)) {
    throw new CatalogQueryError(parameter, "分类 slug 格式无效")
  }
  return value
}

function optionalDateTime(query: RouteQuery, parameter: string) {
  const value = singleValue(query, parameter)
  if (value === undefined) {
    return undefined
  }
  if (!rfc3339Pattern.test(value) || !Number.isFinite(Date.parse(value))) {
    throw new CatalogQueryError(parameter, "时间必须使用 RFC 3339")
  }
  return value
}

function optionalCursor(query: RouteQuery) {
  const cursor = singleValue(query, "cursor")
  if (cursor !== undefined && cursor.length > 2048) {
    throw new CatalogQueryError("cursor", "分页游标过长")
  }
  return cursor
}

function optionalLimit(query: RouteQuery) {
  return optionalInteger(query, "limit", 1, 100)
}

export function paperListQueryFromRoute(query: RouteQuery): PaperListQuery {
  assertAllowedKeys(
    query,
    new Set([
      "q",
      "published_from",
      "published_to",
      "type",
      "topic",
      "method",
      "has_code",
      "has_data",
      "has_benchmark",
      "status",
      "source",
      "sort",
      "limit",
      "cursor",
    ]),
  )

  const q = singleValue(query, "q")
  if (
    q !== undefined
    && (q !== q.trim() || [...q].length > 300)
  ) {
    throw new CatalogQueryError("q", "搜索词必须去除首尾空白且不超过 300 字")
  }
  const publishedFrom = optionalDateTime(query, "published_from")
  const publishedTo = optionalDateTime(query, "published_to")
  if (
    publishedFrom !== undefined
    && publishedTo !== undefined
    && Date.parse(publishedFrom) > Date.parse(publishedTo)
  ) {
    throw new CatalogQueryError(
      "published_from",
      "开始时间不能晚于结束时间",
    )
  }

  const sort = optionalEnum(query, "sort", paperSorts)
  if (sort === "relevance" && q === undefined) {
    throw new CatalogQueryError("sort", "相关度排序必须同时提供搜索词")
  }

  return compact({
    cursor: optionalCursor(query),
    has_benchmark: optionalBoolean(query, "has_benchmark"),
    has_code: optionalBoolean(query, "has_code"),
    has_data: optionalBoolean(query, "has_data"),
    limit: optionalLimit(query),
    method: optionalSlug(query, "method"),
    published_from: publishedFrom,
    published_to: publishedTo,
    q,
    sort,
    source: optionalEnum(query, "source", sources),
    status: optionalEnum(query, "status", lifecycleStatuses),
    topic: optionalSlug(query, "topic"),
    type: optionalEnum(query, "type", paperTypes),
  })
}

export function pageQueryFromRoute(query: RouteQuery): PageQuery {
  assertAllowedKeys(query, new Set(["limit", "cursor"]))
  return compact({
    cursor: optionalCursor(query),
    limit: optionalLimit(query),
  })
}

export function trendQueryFromRoute(query: RouteQuery): TrendQuery {
  assertAllowedKeys(query, new Set(["window_days", "limit", "cursor"]))
  return compact({
    cursor: optionalCursor(query),
    limit: optionalLimit(query),
    window_days: optionalInteger(query, "window_days", 1, 365),
  })
}

export function opportunityQueryFromRoute(query: RouteQuery): OpportunityQuery {
  assertAllowedKeys(query, new Set(["status", "limit", "cursor"]))
  return compact({
    cursor: optionalCursor(query),
    limit: optionalLimit(query),
    status: optionalEnum(query, "status", opportunityStatuses),
  })
}

function compact<T extends object>(value: T): T {
  return Object.fromEntries(
    Object.entries(value).filter(([, item]) => item !== undefined),
  ) as T
}
