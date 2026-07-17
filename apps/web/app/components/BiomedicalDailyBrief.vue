<script setup lang="ts">
import type {
  AnalysisCollection,
} from "~/types/biomedical"
import type { PaperSummary } from "~/types/catalog"

defineProps<{
  brief: AnalysisCollection<PaperSummary>
}>()
</script>

<template>
  <section
    class="intelligence-module daily-brief"
    data-intelligence-module="latest-papers"
    aria-labelledby="daily-brief-heading"
  >
    <div class="section-heading">
      <div>
        <p class="section-kicker">
          新近发布
        </p>
        <h2 id="daily-brief-heading">
          今日新增精选论文
        </h2>
      </div>
      <NuxtLink class="button button--secondary" to="/papers?sort=published_at_desc">
        浏览全部论文
      </NuxtLink>
    </div>

    <IntelligenceModuleMeta :analysis="brief.analysis" />

    <DataState
      v-if="brief.items.length === 0"
      state="empty"
      title="当前窗口没有新增精选论文"
      message="API 已成功返回空集合；页面不会填充示例论文。"
    />
    <div v-else class="catalog-list">
      <CatalogPaperCard
        v-for="paper in brief.items"
        :key="paper.id"
        :paper="paper"
      />
    </div>
  </section>
</template>
