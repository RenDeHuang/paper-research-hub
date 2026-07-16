<script setup lang="ts">
import { formatCatalogDate } from "~/utils/catalogPresentation"
import { trendQueryFromRoute } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"

definePageMeta({
  title: "趋势",
})

useHead({
  title: "趋势 · Paper Research Hub",
  meta: [
    {
      name: "description",
      content: "查看真实公开目录中的论文、Topic 与 Method 趋势排名。",
    },
  ],
})

const route = useRoute()
const router = useRouter()
const client = useCatalogApi()
const windowDays = ref(
  typeof route.query.window_days === "string"
    ? route.query.window_days
    : "30",
)

watch(
  () => route.query.window_days,
  (value) => {
    windowDays.value = typeof value === "string" ? value : "30"
  },
)

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "trends-catalog",
  () =>
    settleCatalogRequest(async () => {
      const query = trendQueryFromRoute(route.query)
      const [papers, topics, methods] = await Promise.all([
        client.listPaperTrends(query),
        client.listTopicTrends(query),
        client.listMethodTrends(query),
      ])
      return { methods, papers, topics }
    }),
  {
    watch: [() => route.fullPath],
  },
)
const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)

async function applyWindow() {
  await router.push({
    path: "/trends",
    query: {
      window_days: windowDays.value,
    },
  })
}
</script>

<template>
  <div class="portal-page">
    <header class="page-header">
      <p class="page-eyebrow">
        分析快照
      </p>
      <h1>趋势</h1>
      <p class="page-lede">
        展示 API 已计算的排名、分数、名次变化与缺失信号；页面不会生成时间序列或补齐趋势。
      </p>
    </header>

    <form class="compact-filter" @submit.prevent="applyWindow">
      <div>
        <label for="trend-window">排名窗口</label>
        <select id="trend-window" v-model="windowDays" name="window_days">
          <option value="7">7 天</option>
          <option value="30">30 天</option>
          <option value="90">90 天</option>
          <option value="180">180 天</option>
          <option value="365">365 天</option>
        </select>
      </div>
      <button class="button button--primary" type="submit">
        更新窗口
      </button>
    </form>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/trends"
      @retry="refresh"
    />

    <template v-if="readyData">
      <section class="analysis-meta" aria-label="趋势生成信息">
        <dl class="detail-list">
          <div>
            <dt>论文趋势生成时间</dt>
            <dd>{{ formatCatalogDate(readyData.papers.generated_at) }}</dd>
          </div>
          <div>
            <dt>Topic 趋势生成时间</dt>
            <dd>{{ formatCatalogDate(readyData.topics.generated_at) }}</dd>
          </div>
          <div>
            <dt>Method 趋势生成时间</dt>
            <dd>{{ formatCatalogDate(readyData.methods.generated_at) }}</dd>
          </div>
          <div>
            <dt>窗口</dt>
            <dd>{{ readyData.papers.window_days }} 天</dd>
          </div>
        </dl>
      </section>

      <div class="trend-sections">
        <TrendRankingList title="论文趋势" :items="readyData.papers.items" />
        <TrendRankingList title="Topic 趋势" :items="readyData.topics.items" />
        <TrendRankingList title="Method 趋势" :items="readyData.methods.items" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.analysis-meta {
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.trend-sections {
  display: grid;
  gap: var(--space-10);
}
</style>
