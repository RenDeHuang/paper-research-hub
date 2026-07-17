<script setup lang="ts">
import { settleCatalogRequest } from "~/utils/catalogResult"

definePageMeta({
  title: "趋势",
})

useHead({
  title: "趋势",
  meta: [
    {
      name: "description",
      content: "查看 medpaperhub 医学生物学学科、引用及疾病、靶点和方法趋势。",
    },
  ],
})

const client = useCatalogApi()
const {
  data: result,
  status,
  refresh,
} = await useAsyncData("trends-home-catalog", () =>
  settleCatalogRequest(() => client.getHome()),
)
const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)
</script>

<template>
  <div class="trend-overview portal-page">
    <header class="page-header">
      <p class="page-eyebrow">
        medpaperhub intelligence
      </p>
      <h1>医学生物学趋势总览</h1>
      <p class="page-lede">
        同一目录快照汇总学科发表率、论文引用增速，以及疾病、靶点和方法实体趋势。每个模块保留 API 给出的独立统计窗口、来源与缺失状态。
      </p>
    </header>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/trends"
      @retry="refresh"
    />

    <template v-if="readyData">
      <div class="trend-overview__primary">
        <SubjectMomentumGrid :momentum="readyData.subject_momentum" />
        <CitationMomentumList :momentum="readyData.citation_momentum" />
      </div>
      <EntityMomentumGrid :momentum="readyData.entity_momentum" />
    </template>
  </div>
</template>

<style scoped>
.page-header {
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

.trend-overview__primary {
  display: grid;
  gap: var(--space-8);
}

@media (min-width: 1440px) {
  .trend-overview__primary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
