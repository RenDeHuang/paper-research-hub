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
  title: "Method",
})

const route = useRoute()
const client = useCatalogApi()

function methodSlug() {
  if (typeof route.params.slug !== "string" || route.params.slug.length === 0) {
    throw new CatalogQueryError("slug", "Method slug 无效")
  }
  return route.params.slug
}

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "method-detail",
  () =>
    settleCatalogRequest(async () => {
      const slug = methodSlug()
      const pagination = pageQueryFromRoute(route.query)
      const [method, papers] = await Promise.all([
        client.getMethod(slug),
        client.listPapers({
          ...pagination,
          limit: 20,
          method: slug,
        }),
      ])
      return { method, papers }
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
    ? presentDataValue(catalogDataValue(readyData.value.method.description))
    : undefined,
)

useHead(() => ({
  title: readyData.value
    ? `${readyData.value.method.name} · 研究方法`
    : "研究方法详情",
}))
</script>

<template>
  <div class="portal-page">
    <div class="page-actions">
      <NuxtLink class="button button--secondary" to="/methods">
        返回 Method
      </NuxtLink>
    </div>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      not-found-action-to="/methods"
      @retry="refresh"
    />

    <template v-if="readyData">
      <header class="page-header">
        <p class="page-eyebrow">
          Method
        </p>
        <h1>{{ readyData.method.name }}</h1>
        <p
          v-if="description"
          class="page-lede"
          :data-value-state="description.state"
        >
          {{ description.text }}
        </p>
        <p class="page-meta">
          {{ readyData.method.paper_count }} 篇关联论文
        </p>
      </header>

      <section class="taxonomy-detail__papers" aria-labelledby="method-papers-heading">
        <h2 id="method-papers-heading">
          关联论文
        </h2>
        <DataState
          v-if="readyData.papers.items.length === 0"
          state="empty"
          title="当前 Method 没有关联论文"
          message="Method 已存在，但论文查询没有返回可见记录。"
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
