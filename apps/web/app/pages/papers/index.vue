<script setup lang="ts">
import { paperListQueryFromRoute } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"

definePageMeta({
  title: "论文",
})

useHead({
  title: "论文目录",
  meta: [
    {
      name: "description",
      content: "使用公开 Go API 搜索并筛选真实论文目录。",
    },
  ],
})

const route = useRoute()
const router = useRouter()
const client = useCatalogApi()
const formReady = ref(false)

onMounted(() => {
  formReady.value = true
})

function routeText(parameter: string) {
  const value = route.query[parameter]
  return typeof value === "string" ? value : ""
}

const filters = reactive({
  published_from: routeText("published_from"),
  published_to: routeText("published_to"),
  q: routeText("q"),
  sort: routeText("sort") || "published_at_desc",
  type: routeText("type"),
})

watch(
  () => route.fullPath,
  () => {
    for (const key of Object.keys(filters) as Array<keyof typeof filters>) {
      filters[key] =
        key === "sort"
          ? (routeText(key) || "published_at_desc")
          : routeText(key)
    }
  },
)

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "papers-catalog",
  () =>
    settleCatalogRequest(() =>
      client.listPapers(paperListQueryFromRoute(route.query)),
    ),
  {
    watch: [() => route.fullPath],
  },
)

const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)

async function applyFilters() {
  const query: Record<string, string> = {}
  for (const [key, value] of Object.entries(filters)) {
    const normalized = value.trim()
    if (normalized.length > 0) {
      query[key] = normalized
    }
  }
  await router.push({ path: "/papers", query })
}
</script>

<template>
  <div class="portal-page papers-page">
    <header class="page-header">
      <p class="page-eyebrow">
        真实公开目录
      </p>
      <h1>论文</h1>
      <p class="page-lede">
        搜索医学生物学已准入期刊中的有效研究论文与综述，并按日期、引用、趋势或相关度排序。
      </p>
    </header>

    <form class="filter-panel" role="search" @submit.prevent="applyFilters">
      <div class="filter-panel__wide">
        <label for="papers-q">搜索词</label>
        <input
          id="papers-q"
          v-model="filters.q"
          name="q"
          type="search"
          autocomplete="off"
          placeholder="标题、摘要、作者或标识符"
        >
      </div>
      <div>
        <label for="published-from">开始时间（RFC 3339）</label>
        <input
          id="published-from"
          v-model="filters.published_from"
          name="published_from"
          type="text"
          inputmode="text"
          placeholder="2026-07-01T00:00:00Z"
        >
      </div>
      <div>
        <label for="published-to">结束时间（RFC 3339）</label>
        <input
          id="published-to"
          v-model="filters.published_to"
          name="published_to"
          type="text"
          inputmode="text"
          placeholder="2026-07-16T23:59:59Z"
        >
      </div>
      <div>
        <label for="paper-type">论文类型</label>
        <select id="paper-type" v-model="filters.type" name="type">
          <option value="">全部类型</option>
          <option value="research_article">研究论文</option>
          <option value="review">综述</option>
        </select>
      </div>
      <div>
        <label for="paper-sort">排序</label>
        <select id="paper-sort" v-model="filters.sort" name="sort">
          <option value="published_at_desc">发布日期</option>
          <option value="citations_desc">引用数</option>
          <option value="trend_desc">趋势分</option>
          <option value="relevance">相关度（需搜索词）</option>
        </select>
      </div>
      <div class="filter-panel__actions">
        <button
          class="button button--primary"
          type="submit"
          :disabled="!formReady"
        >
          应用筛选
        </button>
        <NuxtLink class="button button--secondary" to="/papers">
          清除筛选
        </NuxtLink>
      </div>
    </form>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/papers"
      @retry="refresh"
    />

    <section
      v-if="readyData"
      class="papers-page__results"
      aria-labelledby="results-heading"
    >
      <div class="section-heading">
        <div>
          <p class="section-kicker">
            查询结果
          </p>
          <h2 id="results-heading">
            {{ readyData.pagination.total }} 篇论文
          </h2>
        </div>
      </div>
      <DataState
        v-if="readyData.items.length === 0"
        state="empty"
        title="没有匹配论文"
        message="API 已完成查询，但当前筛选条件没有返回论文。请调整搜索词或筛选。"
        action-label="清除筛选"
        action-to="/papers"
      />
      <div v-else class="catalog-list">
        <CatalogPaperCard
          v-for="paper in readyData.items"
          :key="paper.id"
          :paper="paper"
        />
      </div>
      <CatalogPagination :pagination="readyData.pagination" />
    </section>
  </div>
</template>

<style scoped>
.papers-page__results {
  display: grid;
  gap: var(--space-4);
}
</style>
