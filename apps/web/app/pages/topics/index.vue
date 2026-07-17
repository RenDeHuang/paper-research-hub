<script setup lang="ts">
import { pageQueryFromRoute } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"

definePageMeta({
  title: "Topic",
})

useHead({
  title: "研究主题",
  meta: [
    {
      name: "description",
      content: "浏览真实公开目录中的研究 Topic。",
    },
  ],
})

const route = useRoute()
const client = useCatalogApi()
const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "topics-catalog",
  () =>
    settleCatalogRequest(() =>
      client.listTopics(pageQueryFromRoute(route.query)),
    ),
  {
    watch: [() => route.fullPath],
  },
)
const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)
</script>

<template>
  <div class="portal-page">
    <header class="page-header">
      <p class="page-eyebrow">
        分类目录
      </p>
      <h1>Topic</h1>
      <p class="page-lede">
        Topic 名称、描述与论文数量均来自当前已发布目录。
      </p>
    </header>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/topics"
      @retry="refresh"
    />

    <section v-if="readyData" class="taxonomy-page" aria-labelledby="topics-heading">
      <h2 id="topics-heading" class="visually-hidden">
        Topic 列表
      </h2>
      <DataState
        v-if="readyData.items.length === 0"
        state="empty"
        title="当前目录暂无 Topic"
        message="API 已成功返回，但已发布目录中还没有 Topic。"
      />
      <div v-else class="taxonomy-grid">
        <TaxonomyCard
          v-for="item in readyData.items"
          :key="item.id"
          :item="item"
          kind="topics"
        />
      </div>
      <CatalogPagination :pagination="readyData.pagination" />
    </section>
  </div>
</template>
