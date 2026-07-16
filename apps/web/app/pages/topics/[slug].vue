<script setup lang="ts">
import {
  catalogDataValue,
} from "~/utils/catalogPresentation"
import {
  CatalogQueryError,
  pageQueryFromRoute,
} from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"
import { presentDataValue } from "~/utils/dataValue"

definePageMeta({
  title: "Topic",
})

const route = useRoute()
const client = useCatalogApi()

function topicSlug() {
  if (typeof route.params.slug !== "string" || route.params.slug.length === 0) {
    throw new CatalogQueryError("slug", "Topic slug 无效")
  }
  return route.params.slug
}

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "topic-detail",
  () =>
    settleCatalogRequest(async () => {
      const slug = topicSlug()
      const pagination = pageQueryFromRoute(route.query)
      const [topic, papers] = await Promise.all([
        client.getTopic(slug),
        client.listPapers({
          ...pagination,
          limit: 20,
          topic: slug,
        }),
      ])
      return { papers, topic }
    }),
  {
    watch: [() => route.params.slug, () => route.query.cursor],
  },
)
const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)
const description = computed(() =>
  readyData.value
    ? presentDataValue(catalogDataValue(readyData.value.topic.description))
    : undefined,
)

useHead(() => ({
  title: readyData.value
    ? `${readyData.value.topic.name} · Topic · Paper Research Hub`
    : "Topic 详情 · Paper Research Hub",
}))
</script>

<template>
  <div class="portal-page">
    <div class="page-actions">
      <NuxtLink class="button button--secondary" to="/topics">
        返回 Topic
      </NuxtLink>
    </div>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      not-found-action-to="/topics"
      @retry="refresh"
    />

    <template v-if="readyData">
      <header class="page-header">
        <p class="page-eyebrow">
          Topic
        </p>
        <h1>{{ readyData.topic.name }}</h1>
        <p
          v-if="description"
          class="page-lede"
          :data-value-state="description.state"
        >
          {{ description.text }}
        </p>
        <p class="page-meta">
          {{ readyData.topic.paper_count }} 篇关联论文
        </p>
      </header>

      <section class="taxonomy-detail__papers" aria-labelledby="topic-papers-heading">
        <h2 id="topic-papers-heading">
          关联论文
        </h2>
        <DataState
          v-if="readyData.papers.items.length === 0"
          state="empty"
          title="当前 Topic 没有关联论文"
          message="Topic 已存在，但论文查询没有返回可见记录。"
        />
        <div v-else class="catalog-list">
          <CatalogPaperCard
            v-for="paper in readyData.papers.items"
            :key="paper.id"
            :paper="paper"
          />
        </div>
        <CatalogPagination :pagination="readyData.papers.pagination" />
      </section>
    </template>
  </div>
</template>

<style scoped>
.page-actions {
  display: flex;
}

.page-meta {
  margin: 0;
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
  font-weight: 700;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-style: italic;
}

.taxonomy-detail__papers {
  display: grid;
  gap: var(--space-4);
}

.taxonomy-detail__papers h2 {
  margin: 0;
}
</style>
