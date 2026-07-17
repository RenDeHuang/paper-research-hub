<script setup lang="ts">
import type {
  AnalysisCollection,
  JournalActivityItem,
} from "~/types/biomedical"
import {
  catalogDataValue,
  percentageDataValue,
} from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

defineProps<{
  activity: AnalysisCollection<JournalActivityItem>
}>()
</script>

<template>
  <section
    class="intelligence-module journal-activity"
    data-intelligence-module="active-journals"
    aria-labelledby="journal-activity-heading"
  >
    <div class="section-heading">
      <div>
        <p class="section-kicker">
          发表节奏
        </p>
        <h2 id="journal-activity-heading">
          活跃期刊
        </h2>
      </div>
      <NuxtLink class="button button--secondary" to="/journals">
        查看全部期刊
      </NuxtLink>
    </div>

    <IntelligenceModuleMeta :analysis="activity.analysis" />

    <DataState
      v-if="activity.items.length === 0"
      state="empty"
      title="当前窗口没有活跃期刊"
      message="API 已成功返回空集合；页面不会把未知发表节奏显示为零。"
    />
    <ol v-else>
      <li v-for="item in activity.items" :key="item.journal.id">
        <div>
          <h3>
            <NuxtLink :to="`/journals/${item.journal.slug}`">
              {{ item.journal.title }}
            </NuxtLink>
          </h3>
          <p>
            JCR {{ item.journal.jcr_metric_year }}
          </p>
        </div>
        <dl>
          <div>
            <dt>窗口论文</dt>
            <dd :data-value-state="catalogDataValue(item.paper_count).state">
              {{ presentDataValue(catalogDataValue(item.paper_count)).text }}
            </dd>
          </div>
          <div>
            <dt>相对变化</dt>
            <dd :data-value-state="percentageDataValue(item.publication_change_ratio).state">
              {{ presentDataValue(percentageDataValue(item.publication_change_ratio)).text }}
            </dd>
          </div>
          <div>
            <dt>JIF</dt>
            <dd :data-value-state="catalogDataValue(item.journal.jif).state">
              {{ presentDataValue(catalogDataValue(item.journal.jif)).text }}
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
p,
dl,
dd {
  margin: 0;
}

h3 {
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
}

h3 a {
  display: inline-flex;
  min-height: 44px;
  align-items: center;
  color: inherit;
}

p,
dt {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
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

@media (min-width: 768px) {
  li {
    grid-template-columns: minmax(0, 1fr) minmax(300px, 0.8fr);
    align-items: center;
  }
}
</style>
