<script setup lang="ts">
import { opportunityQueryFromRoute } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"

definePageMeta({
  title: "研究机会",
})

useHead({
  title: "研究机会",
  meta: [
    {
      name: "description",
      content: "查看真实 API 生成、带规则与估计边界的研究机会。",
    },
  ],
})

const route = useRoute()
const router = useRouter()
const client = useCatalogApi()
const selectedStatus = ref(
  typeof route.query.status === "string" ? route.query.status : "",
)

watch(
  () => route.query.status,
  (value) => {
    selectedStatus.value = typeof value === "string" ? value : ""
  },
)

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "opportunities-catalog",
  () =>
    settleCatalogRequest(() =>
      client.listResearchOpportunities(
        opportunityQueryFromRoute(route.query),
      ),
    ),
  {
    watch: [() => route.fullPath],
  },
)
const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)

async function applyStatus() {
  await router.push({
    path: "/opportunities",
    query:
      selectedStatus.value.length > 0
        ? { status: selectedStatus.value }
        : {},
  })
}
</script>

<template>
  <div class="portal-page">
    <header class="page-header">
      <p class="page-eyebrow">
        规则、估计、覆盖率与支持工作
      </p>
      <h1>研究机会</h1>
      <p class="page-lede">
        所有结论都来自 API 发布的规则命中、估计值、置信区间、覆盖率和支持工作 ID；页面不会回退到二维伪矩阵或手工推断。
      </p>
    </header>

    <form class="compact-filter" @submit.prevent="applyStatus">
      <div>
        <label for="opportunity-status">推荐状态</label>
        <select
          id="opportunity-status"
          v-model="selectedStatus"
          name="status"
        >
          <option value="">全部状态</option>
          <option value="worth_pursuing">值得做</option>
          <option value="proceed_with_caution">谨慎做</option>
          <option value="not_recommended_now">当前不建议做</option>
          <option value="insufficient_evidence">证据不足</option>
        </select>
      </div>
      <button class="button button--primary" type="submit">
        应用筛选
      </button>
    </form>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/opportunities"
      @retry="refresh"
    />

    <template v-if="readyData">
      <ResearchOpportunityBoard
        :collection="readyData"
        empty-action-label="清除筛选"
        empty-action-to="/opportunities"
        empty-message="API 已成功返回，但当前筛选条件下没有研究机会记录。"
      />
      <CatalogPagination :pagination="readyData.pagination" />
    </template>
  </div>
</template>
