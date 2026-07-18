<script setup lang="ts">
import type {
  AnalysisCollection,
  EntityMomentumItem,
} from "~/types/biomedical"
import { catalogDataValue } from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

withDefaults(defineProps<{
  compact?: boolean
  momentum: AnalysisCollection<EntityMomentumItem>
}>(), {
  compact: false,
})

const entityTypeLabels = {
  disease: "疾病",
  method: "方法",
  publication_type: "Publication Type",
  study_design: "研究设计",
  target: "靶点",
} as const

const numberFormatter = new Intl.NumberFormat("zh-CN", {
  maximumFractionDigits: 3,
})

function estimateText(item: EntityMomentumItem) {
  const value = catalogDataValue(item.estimate)
  if (value.state !== "known") {
    return presentDataValue(value).text
  }
  return `${numberFormatter.format(value.value)}×`
}

function intervalText(interval: EntityMomentumItem["confidence_interval"]) {
  return `${numberFormatter.format(interval.lower)}–${numberFormatter.format(interval.upper)}`
}
</script>

<template>
  <section
    class="intelligence-module entity-momentum"
    data-intelligence-module="entity-momentum"
    aria-labelledby="entity-momentum-heading"
  >
    <div class="section-heading">
      <div>
        <p class="section-kicker">
          {{ compact ? "主题 / 方法 / 研究设计" : "医学语义与研究方法" }}
        </p>
        <h2 id="entity-momentum-heading">
          {{ compact ? "最近 7 天趋势" : "热门疾病/靶点/方法" }}
        </h2>
      </div>
    </div>

    <IntelligenceModuleMeta
      v-if="!compact"
      :analysis="momentum.analysis"
    />

    <p
      v-if="compact && momentum.items.length === 0"
      class="entity-momentum__empty"
    >
      暂无趋势数据
    </p>
    <DataState
      v-else-if="momentum.items.length === 0"
      state="empty"
      title="当前窗口没有实体趋势"
      message="API 已成功返回空集合；页面不会从论文标题推断疾病、靶点或方法。"
    />
    <div v-else class="entity-momentum__grid">
      <article
        v-for="item in momentum.items"
        :key="`${item.entity_type}:${item.label}`"
      >
        <p>{{ entityTypeLabels[item.entity_type] }}</p>
        <h3>{{ item.label }}</h3>
        <p class="metric-card__model">
          模型：{{ item.model_family }}
        </p>
        <strong :data-value-state="catalogDataValue(item.estimate).state">
          {{ estimateText(item) }}
        </strong>
        <dl>
          <div>
            <dt>最近窗口</dt>
            <dd :data-value-state="catalogDataValue(item.recent_count).state">
              {{ presentDataValue(catalogDataValue(item.recent_count)).text }}
            </dd>
          </div>
          <div>
            <dt>基线窗口</dt>
            <dd :data-value-state="catalogDataValue(item.baseline_count).state">
              {{ presentDataValue(catalogDataValue(item.baseline_count)).text }}
            </dd>
          </div>
          <div>
            <dt>95% 区间</dt>
            <dd>{{ intervalText(item.confidence_interval) }}</dd>
          </div>
          <div>
            <dt>p 值</dt>
            <dd :data-value-state="catalogDataValue(item.p_value).state">
              {{ presentDataValue(catalogDataValue(item.p_value)).text }}
            </dd>
          </div>
          <div>
            <dt>校正 p 值</dt>
            <dd :data-value-state="catalogDataValue(item.adjusted_p_value).state">
              {{ presentDataValue(catalogDataValue(item.adjusted_p_value)).text }}
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
.entity-momentum {
  display: grid;
  gap: var(--space-4);
}

.entity-momentum__grid {
  display: grid;
  gap: var(--space-3);
}

article {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

p,
h3 {
  margin: 0;
}

p {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}

.entity-momentum__empty {
  padding: var(--space-5) 0;
}

.metric-card__model {
  color: var(--color-text-muted);
}

h3 {
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
  overflow-wrap: anywhere;
}

strong {
  color: var(--color-positive);
  font-family: var(--font-data);
  font-size: var(--text-xl-size);
  font-variant-numeric: tabular-nums;
}

dl {
  display: grid;
  gap: var(--space-2);
  margin: 0;
}

dl > div {
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
  .entity-momentum__grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1440px) {
  .entity-momentum__grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
