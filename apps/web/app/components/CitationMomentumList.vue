<script setup lang="ts">
import type {
  AnalysisCollection,
  CitationMomentumItem,
} from "~/types/biomedical"
import {
  catalogDataValue,
  percentageDataValue,
} from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

defineProps<{
  momentum: AnalysisCollection<CitationMomentumItem>
}>()
</script>

<template>
  <section
    class="intelligence-module citation-momentum"
    data-intelligence-module="citation-momentum"
    aria-labelledby="citation-momentum-heading"
  >
    <div class="section-heading">
      <div>
        <p class="section-kicker">
          来源快照差值
        </p>
        <h2 id="citation-momentum-heading">
          引用增长
        </h2>
      </div>
    </div>

    <IntelligenceModuleMeta :analysis="momentum.analysis" />

    <DataState
      v-if="momentum.items.length === 0"
      state="empty"
      title="当前窗口没有可复算引用增长"
      message="至少需要两个来源快照；页面不会用引用总量代替增量。"
    />
    <ol v-else>
      <li v-for="item in momentum.items" :key="item.paper.id">
        <h3>
          <NuxtLink :to="`/papers/${item.paper.id}`">
            {{ item.paper.title }}
          </NuxtLink>
        </h3>
        <dl>
          <div>
            <dt>引用增量</dt>
            <dd :data-value-state="catalogDataValue(item.citation_delta).state">
              {{ presentDataValue(catalogDataValue(item.citation_delta)).text }}
            </dd>
          </div>
          <div>
            <dt>每日引用</dt>
            <dd :data-value-state="catalogDataValue(item.citations_per_day).state">
              {{ presentDataValue(catalogDataValue(item.citations_per_day)).text }}
            </dd>
          </div>
          <div>
            <dt>同 cohort 百分位</dt>
            <dd :data-value-state="percentageDataValue(item.cohort_percentile).state">
              {{ presentDataValue(percentageDataValue(item.cohort_percentile)).text }}
            </dd>
          </div>
        </dl>
      </li>
    </ol>
  </section>
</template>

<style scoped>
ol {
  display: grid;
  gap: var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}

li {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

h3,
dl,
dd {
  margin: 0;
}

h3 {
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
  overflow-wrap: anywhere;
}

h3 a {
  display: inline-flex;
  min-height: 44px;
  align-items: center;
  color: inherit;
}

dl {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: var(--space-2);
}

dl > div {
  display: grid;
  gap: var(--space-1);
}

dt {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}

dd {
  color: var(--color-text-strong);
  font-family: var(--font-data);
  font-variant-numeric: tabular-nums;
  font-weight: 750;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-family: var(--font-ui);
  font-style: italic;
}
</style>
