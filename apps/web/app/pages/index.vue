<script setup lang="ts">
import type { DataValue } from "~/utils/dataValue"
import {
  catalogDataValue,
  formatCatalogDate,
  percentageDataValue,
} from "~/utils/catalogPresentation"
import { settleCatalogRequest } from "~/utils/catalogResult"

useHead({
  title: "Paper Research Hub",
  meta: [
    {
      name: "description",
      content: "以来源、覆盖和证据为边界的论文研究情报门户。",
    },
  ],
})

const client = useCatalogApi()
const {
  data: result,
  status,
  refresh,
} = await useAsyncData("home-catalog", () =>
  settleCatalogRequest(async () => {
    const [stats, papers] = await Promise.all([
      client.getStats(),
      client.listPapers({
        limit: 3,
        sort: "published_at_desc",
      }),
    ])
    return { papers, stats }
  }),
)

const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)
const syncStatus = computed<{
  coverage: DataValue<number | string>
  dataRange: DataValue<string>
  updatedAt: DataValue<string>
}>(() => {
  if (readyData.value === undefined) {
    return {
      coverage: { label: "尚未加载", state: "unknown" },
      dataRange: { label: "API 未提供时间范围", state: "unknown" },
      updatedAt: { label: "尚未加载", state: "unknown" },
    }
  }
  return {
    coverage: percentageDataValue(readyData.value.stats.with_code_ratio),
    dataRange: { label: "API 未提供时间范围", state: "unknown" },
    updatedAt: {
      state: "known",
      value: formatCatalogDate(readyData.value.stats.generated_at),
    },
  }
})
const stats = computed(() => {
  if (readyData.value === undefined) {
    return []
  }
  return [
    {
      label: "论文总数",
      value: catalogDataValue(readyData.value.stats.papers_total),
    },
    {
      label: "近 7 日论文",
      value: catalogDataValue(readyData.value.stats.papers_last_7_days),
    },
    {
      label: "Topic",
      value: catalogDataValue(readyData.value.stats.topics_total),
    },
    {
      label: "Method",
      value: catalogDataValue(readyData.value.stats.methods_total),
    },
  ]
})
</script>

<template>
  <div class="home-page portal-page">
    <section class="home-page__intro" aria-labelledby="home-heading">
      <p class="page-eyebrow">
        Paper Research Hub
      </p>
      <h1 id="home-heading">
        论文研究情报，从发现到证据
      </h1>
      <p class="page-lede">
        搜索真实公开目录，核查论文、Topic、Method、趋势、研究机会及其覆盖边界。
      </p>
      <SearchCommand
        suggestions-state="unavailable"
        unavailable-message="当前 API 未提供搜索建议端点；提交后将执行真实论文检索。"
      />
      <SyncStatus
        :updated-at="syncStatus.updatedAt"
        :data-range="syncStatus.dataRange"
        :coverage="syncStatus.coverage"
      />
    </section>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      @retry="refresh"
    />

    <template v-if="readyData">
      <section class="home-page__stats" aria-labelledby="stats-heading">
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              当前目录
            </p>
            <h2 id="stats-heading">
              已发布数据概览
            </h2>
          </div>
          <NuxtLink class="button button--secondary" to="/papers">
            浏览全部论文
          </NuxtLink>
        </div>
        <dl class="stat-grid">
          <div v-for="item in stats" :key="item.label">
            <dt>{{ item.label }}</dt>
            <dd :data-value-state="item.value.state">
              {{ presentDataValue(item.value).text }}
            </dd>
          </div>
        </dl>
      </section>

      <section class="home-page__papers" aria-labelledby="latest-heading">
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              最新发布
            </p>
            <h2 id="latest-heading">
              目录中的最近论文
            </h2>
          </div>
        </div>
        <DataState
          v-if="readyData.papers.items.length === 0"
          state="empty"
          title="当前目录暂无论文"
          message="API 已成功返回，但已发布目录中还没有论文记录。"
        />
        <div v-else class="catalog-list">
          <CatalogPaperCard
            v-for="paper in readyData.papers.items"
            :key="paper.id"
            :paper="paper"
          />
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.home-page__intro {
  display: grid;
  gap: var(--space-4);
  max-width: 840px;
}

h1 {
  max-width: 18ch;
  margin: 0;
  color: var(--color-text-strong);
  font-size: clamp(var(--text-2xl-size), 7vw, var(--text-4xl-size));
  line-height: var(--text-4xl-line);
  overflow-wrap: anywhere;
}

.home-page__stats,
.home-page__papers {
  display: grid;
  gap: var(--space-4);
}
</style>
