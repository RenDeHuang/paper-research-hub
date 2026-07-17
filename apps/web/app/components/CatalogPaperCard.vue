<script setup lang="ts">
import type { DataValue } from "~/utils/dataValue"
import type { PaperSummary } from "~/types/catalog"
import {
  catalogDataValue,
  formatCatalogDate,
  paperAuthors,
  paperSources,
  paperStatusPresentation,
  paperTaxonomyLabels,
  percentageDataValue,
} from "~/utils/catalogPresentation"

const props = defineProps<{
  paper: PaperSummary
}>()

const authors = computed(() => paperAuthors(props.paper.authors))
const publishedAt = computed<DataValue<string>>(() => {
  const value = catalogDataValue(
    props.paper.published_at,
    "发布日期字段未覆盖",
  )
  if (value.state !== "known") {
    return value
  }
  return {
    state: "known",
    value: formatCatalogDate(value.value),
  }
})
const sourceType = computed<DataValue<string>>(() => {
  const value = paperSources(props.paper.source_provenance)
  if (value.state !== "known") {
    return value
  }
  return {
    state: "known",
    value: value.value.join("、"),
  }
})
const summary = computed(() =>
  catalogDataValue(
    props.paper.abstract_snippet ?? props.paper.abstract,
    "摘要字段未覆盖",
  ),
)
const tags = computed(() => [
  ...paperTaxonomyLabels(props.paper.subjects).map((label) => ({
    category: "Subject",
    label,
  })),
  ...paperTaxonomyLabels(props.paper.topics).map((label) => ({
    category: "Topic",
    label,
  })),
  ...paperTaxonomyLabels(props.paper.methods).map((label) => ({
    category: "Method",
    label,
  })),
  ...(props.paper.publication_types ?? []).map((label) => ({
    category: "Publication Type",
    label,
  })),
])
const venue = computed<DataValue<string> | undefined>(() =>
  props.paper.journal === undefined
    ? undefined
    : {
        state: "known",
        value: props.paper.journal.title,
      },
)
const evidence = computed(() => [
  {
    label: "代码",
    value: catalogDataValue(props.paper.has_code),
  },
  {
    label: "数据",
    value: catalogDataValue(props.paper.has_data),
  },
  {
    label: "Benchmark",
    value: catalogDataValue(props.paper.has_benchmark),
  },
])
const metrics = computed(() => {
  const values = []
  if (props.paper.citation_count !== undefined) {
    values.push({
      label: "引用数",
      value: catalogDataValue(props.paper.citation_count),
    })
  }
  if (props.paper.trend_score !== undefined) {
    values.push({
      label: "趋势分",
      value: percentageDataValue(props.paper.trend_score),
    })
  }
  return values
})
</script>

<template>
  <PaperCard
    :title="paper.title"
    :detail-to="`/papers/${paper.id}`"
    :authors="authors"
    :published-at="publishedAt"
    :source-type="sourceType"
    :status="paperStatusPresentation(paper.status)"
    :summary="summary"
    :tags="tags"
    :venue="venue"
    :evidence="evidence"
    :metrics="metrics"
  />
</template>
