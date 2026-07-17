<script setup lang="ts">
import type {
  AnalysisCollection,
  SubjectTrendEstimate,
} from "~/types/biomedical"
import { catalogDataValue } from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

defineProps<{
  momentum: AnalysisCollection<SubjectTrendEstimate>
}>()

function estimateText(item: SubjectTrendEstimate) {
  const value = catalogDataValue(item.estimate)
  if (value.state !== "known") {
    return presentDataValue(value).text
  }
  return `${value.value.toFixed(2)}×`
}
</script>

<template>
  <section
    class="intelligence-module subject-momentum"
    data-intelligence-module="subject-momentum"
    aria-labelledby="subject-momentum-heading"
  >
    <div class="section-heading">
      <div>
        <p class="section-kicker">
          发表率变化
        </p>
        <h2 id="subject-momentum-heading">
          学科趋势
        </h2>
      </div>
      <NuxtLink class="button button--secondary" to="/subjects">
        查看全部学科
      </NuxtLink>
    </div>

    <IntelligenceModuleMeta :analysis="momentum.analysis" />

    <DataState
      v-if="momentum.items.length === 0"
      state="empty"
      title="当前窗口没有学科趋势"
      message="API 已成功返回空集合；没有基线时不会显示 0% 增长。"
    />
    <div v-else class="subject-momentum__grid">
      <article
        v-for="item in momentum.items"
        :key="item.subject?.id ?? item.label"
        class="metric-card"
      >
        <p class="metric-card__label">
          {{ item.label ?? "论文发表率比" }}
        </p>
        <h3>
          <NuxtLink
            v-if="item.subject"
            :to="`/subjects/${item.subject.slug}`"
          >
            {{ item.subject.name }}
          </NuxtLink>
          <template v-else>
            学科标识未覆盖
          </template>
        </h3>
        <p
          class="metric-card__value"
          :data-value-state="catalogDataValue(item.estimate).state"
        >
          {{ estimateText(item) }}
        </p>
        <dl>
          <div>
            <dt>95% 区间</dt>
            <dd>
              {{ item.confidence_interval.lower.toFixed(2) }}–{{ item.confidence_interval.upper.toFixed(2) }}
            </dd>
          </div>
          <div>
            <dt>独立期刊</dt>
            <dd :data-value-state="catalogDataValue(item.independent_journal_count).state">
              {{ presentDataValue(catalogDataValue(item.independent_journal_count)).text }}
            </dd>
          </div>
          <div>
            <dt>独立团队</dt>
            <dd :data-value-state="catalogDataValue(item.independent_team_count).state">
              {{ presentDataValue(catalogDataValue(item.independent_team_count)).text }}
            </dd>
          </div>
        </dl>
      </article>
    </div>
  </section>
</template>

<style scoped>
.subject-momentum__grid {
  display: grid;
  gap: var(--space-3);
}

.metric-card {
  display: grid;
  gap: var(--space-2);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.metric-card > *,
.metric-card dl,
.metric-card dd {
  margin: 0;
}

.metric-card__label {
  color: var(--color-info);
  font-size: var(--text-xs-size);
  font-weight: 800;
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

.metric-card__value {
  color: var(--color-positive);
  font-family: var(--font-data);
  font-size: var(--text-2xl-size);
  font-variant-numeric: tabular-nums;
  font-weight: 800;
}

.metric-card dl {
  display: grid;
  gap: var(--space-2);
}

.metric-card dl > div {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--space-3);
}

dt,
dd {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}

dd {
  color: var(--color-text-strong);
  font-variant-numeric: tabular-nums;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-family: var(--font-ui);
  font-size: var(--text-base-size);
  font-style: italic;
}

@media (min-width: 768px) {
  .subject-momentum__grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1440px) {
  .subject-momentum__grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
