<script setup lang="ts">
import { pageQueryFromRoute } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"

definePageMeta({
  title: "Method",
})

useHead({
  title: "Method · Paper Research Hub",
  meta: [
    {
      name: "description",
      content: "浏览真实公开目录中的研究 Method。",
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
  "methods-catalog",
  () =>
    settleCatalogRequest(() =>
      client.listMethods(pageQueryFromRoute(route.query)),
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
        方法目录
      </p>
      <h1>Method</h1>
      <p class="page-lede">
        Method 名称、描述与论文数量均来自当前已发布目录。
      </p>
    </header>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/methods"
      @retry="refresh"
    />

    <section v-if="readyData" class="taxonomy-page" aria-labelledby="methods-heading">
      <h2 id="methods-heading" class="visually-hidden">
        Method 列表
      </h2>
      <DataState
        v-if="readyData.items.length === 0"
        state="empty"
        title="当前目录暂无 Method"
        message="API 已成功返回，但已发布目录中还没有 Method。"
      />
      <div v-else class="taxonomy-grid">
        <TaxonomyCard
          v-for="item in readyData.items"
          :key="item.id"
          :item="item"
          kind="methods"
        />
      </div>
      <CatalogPagination :pagination="readyData.pagination" />
    </section>
  </div>
</template>
