import type {
  AuthorReference,
  CatalogValue,
  PaperLifecycleStatus,
  ResearchOpportunityStatus,
  TaxonomyReference,
  TrendItem,
} from "~/types/catalog"
import type { components } from "~/types/openapi.generated"
import type {
  DataValue,
  KnownDataValue,
} from "~/utils/dataValue"

export function catalogDataValue<T extends KnownDataValue>(
  value: CatalogValue<T> | undefined,
  unknownLabel = "未覆盖",
): DataValue<T> {
  if (value === undefined) {
    return {
      label: unknownLabel,
      state: "unknown",
    }
  }
  if (
    typeof value === "object"
    && value !== null
    && !Array.isArray(value)
    && "state" in value
  ) {
    if (value.state === "known") {
      return {
        state: "known",
        value: value.value,
      }
    }
    return { state: value.state }
  }
  return {
    state: "known",
    value,
  }
}

export function percentageDataValue(
  value: CatalogValue<number> | undefined,
): DataValue<number> {
  const normalized = catalogDataValue(value)
  if (normalized.state !== "known") {
    return normalized
  }
  return {
    state: "known",
    suffix: "%",
    value: Math.round(normalized.value * 1000) / 10,
  }
}

export function paperAuthors(
  authors: AuthorReference[] | undefined,
): DataValue<string[]> {
  if (authors === undefined) {
    return {
      label: "作者字段未覆盖",
      state: "unknown",
    }
  }
  return {
    state: "known",
    value: authors.map((author) => {
      if (author.name !== undefined) {
        return author.name
      }
      if (author.display_name !== undefined) {
        return author.display_name
      }
      return author.id
    }),
  }
}

export function paperTaxonomyLabels(
  values: Array<TaxonomyReference | string> | undefined,
) {
  if (values === undefined) {
    return []
  }
  return values.map((value) =>
    typeof value === "string" ? value : value.name,
  )
}

export function paperSources(
  provenance:
    | components["schemas"]["SourceProvenanceCatalogValue"]
    | undefined,
): DataValue<string[]> {
  if (provenance === undefined) {
    return {
      label: "来源字段未覆盖",
      state: "unknown",
    }
  }
  if (provenance.state !== "known") {
    return { state: provenance.state }
  }
  const values = provenance.value.map((source) => source.source)
  return {
    state: "known",
    value: [...new Set(values)],
  }
}

export function formatCatalogDate(value: string) {
  const date = new Date(value)
  if (!Number.isFinite(date.valueOf())) {
    throw new Error(`API 返回了无效时间: ${value}`)
  }
  return new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "UTC",
  }).format(date)
}

export function paperStatusPresentation(status: string) {
  const definitions: Record<
    PaperLifecycleStatus,
    {
      description?: string
      label: string
      tone: "caution" | "negative" | "positive"
    }
  > = {
    active: {
      label: "有效",
      tone: "positive",
    },
    rejected: {
      description: "该记录未通过当前纳入规则。",
      label: "已拒绝",
      tone: "negative",
    },
    retracted: {
      description: "该论文已被撤稿，不进入推荐榜。",
      label: "已撤稿",
      tone: "negative",
    },
    superseded: {
      description: "该记录已被更新版本替代。",
      label: "已被替代",
      tone: "caution",
    },
    withdrawn: {
      description: "该论文已撤回，不进入推荐榜。",
      label: "已撤回",
      tone: "negative",
    },
  }
  if (status in definitions) {
    return {
      code: status as PaperLifecycleStatus,
      ...definitions[status as PaperLifecycleStatus],
    }
  }
  return {
    description: `API 返回状态：${status}`,
    label: status,
    tone: "caution" as const,
  }
}

export function opportunityStatusPresentation(
  status: ResearchOpportunityStatus,
) {
  return {
    insufficient_evidence: "证据不足",
    not_recommended_now: "当前不建议做",
    proceed_with_caution: "谨慎做",
    worth_pursuing: "值得做",
  }[status]
}

export function opportunityMatrixStatus(
  status: ResearchOpportunityStatus,
) {
  return status.replaceAll("_", "-") as
    | "insufficient-evidence"
    | "not-recommended-now"
    | "proceed-with-caution"
    | "worth-pursuing"
}

export function trendItemIdentity(item: TrendItem) {
  if (item.paper !== undefined) {
    return {
      label: item.paper.title,
      to: `/papers/${item.paper.id}`,
      type: "论文",
    }
  }
  if (item.topic !== undefined) {
    return {
      label: item.topic.name,
      to: `/topics/${item.topic.slug}`,
      type: "Topic",
    }
  }
  if (item.method !== undefined) {
    return {
      label: item.method.name,
      to: `/methods/${item.method.slug}`,
      type: "Method",
    }
  }
  return {
    label: item.subject_id ?? "实体标识未覆盖",
    type: "研究实体",
  }
}

export function missingSignalLabels(
  values: Array<{ signal: string } | string> | undefined,
) {
  return (values ?? []).map((value) =>
    typeof value === "string" ? value : value.signal,
  )
}
