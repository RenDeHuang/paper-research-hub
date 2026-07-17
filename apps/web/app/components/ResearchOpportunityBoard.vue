<script setup lang="ts">
import type { ResearchOpportunity } from "~/types/catalog"
import type { AnalysisCollection } from "~/types/biomedical"
import {
  formatCatalogDate,
  opportunityStatusPresentation,
  percentageDataValue,
} from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

const _props = withDefaults(
  defineProps<{
    collection: AnalysisCollection<ResearchOpportunity>
    emptyActionLabel?: string
    emptyActionTo?: string
    emptyMessage?: string
    emptyTitle?: string
  }>(),
  {
    emptyActionLabel: undefined,
    emptyActionTo: undefined,
    emptyMessage: "API 已成功返回空集合；当前没有可展示的研究机会。",
    emptyTitle: "当前没有研究机会",
  },
)

const numberFormatter = new Intl.NumberFormat("zh-CN", {
  maximumFractionDigits: 3,
})

function formatNumber(value: number) {
  return numberFormatter.format(value)
}

function formatInterval(interval: { lower: number; upper: number }) {
  return `${formatNumber(interval.lower)}–${formatNumber(interval.upper)}`
}

function coverageText(value: ResearchOpportunity["coverage_ratio"]) {
  return presentDataValue(percentageDataValue(value)).text
}
</script>

<template>
  <section class="research-opportunity-board">
    <IntelligenceModuleMeta :analysis="collection.analysis" />

    <DataState
      v-if="collection.items.length === 0"
      state="empty"
      :title="emptyTitle"
      :message="emptyMessage"
      :action-label="emptyActionLabel"
      :action-to="emptyActionTo"
    />

    <div v-else class="research-opportunity-board__grid">
      <article
        v-for="item in collection.items"
        :key="item.id"
        class="research-opportunity-card"
      >
        <div class="research-opportunity-card__topline">
          <span
            class="status-badge"
            :class="{
              'status-badge--positive': item.status === 'worth_pursuing',
              'status-badge--caution': item.status === 'proceed_with_caution' || item.status === 'insufficient_evidence',
              'status-badge--negative': item.status === 'not_recommended_now',
            }"
          >
            {{ opportunityStatusPresentation(item.status) }}
          </span>
          <code>{{ item.trigger_rule.code }} · v{{ item.trigger_rule.version }}</code>
        </div>

        <div class="research-opportunity-card__headline">
          <h3>{{ item.title }}</h3>
          <p
            class="research-opportunity-card__summary"
            :data-value-state="item.summary === undefined ? 'unknown' : 'known'"
          >
            {{ item.summary ?? '摘要字段未覆盖' }}
          </p>
        </div>

        <dl class="research-opportunity-card__meta">
          <div>
            <dt>目标类型</dt>
            <dd>{{ item.target_kind }}</dd>
          </div>
          <div>
            <dt>目标 ID</dt>
            <dd><code>{{ item.target_id }}</code></dd>
          </div>
          <div>
            <dt>分析 run ID</dt>
            <dd><code>{{ item.analysis_run_id }}</code></dd>
          </div>
          <div>
            <dt>覆盖率</dt>
            <dd :data-value-state="percentageDataValue(item.coverage_ratio).state">
              {{ coverageText(item.coverage_ratio) }}
            </dd>
          </div>
          <div>
            <dt>生成时间</dt>
            <dd>{{ formatCatalogDate(item.generated_at) }}</dd>
          </div>
          <div>
            <dt>公式版本</dt>
            <dd>{{ item.formula_version }}</dd>
          </div>
        </dl>

        <section class="research-opportunity-card__section">
          <h4>估计</h4>
          <ul class="research-opportunity-card__estimates">
            <li
              v-for="estimate in item.estimates"
              :key="estimate.metric"
            >
              <strong>{{ estimate.metric }}</strong>
              <span>{{ formatNumber(estimate.value) }}</span>
              <small>{{ formatInterval(estimate.confidence_interval) }}</small>
            </li>
          </ul>
        </section>

        <section class="research-opportunity-card__section">
          <h4>支持工作 ID</h4>
          <ul class="research-opportunity-card__work-ids">
            <li
              v-for="workID in item.supporting_work_ids"
              :key="workID"
            >
              <code>{{ workID }}</code>
            </li>
          </ul>
        </section>

        <section class="research-opportunity-card__section">
          <h4>缺失信号</h4>
          <p
            v-if="item.missing_signals === undefined"
            data-value-state="unknown"
          >
            未覆盖
          </p>
          <p v-else-if="item.missing_signals.length === 0">
            API 明确返回空集合
          </p>
          <ul v-else class="research-opportunity-card__list">
            <li
              v-for="signal in item.missing_signals"
              :key="signal"
            >
              {{ signal }}
            </li>
          </ul>
        </section>

        <section class="research-opportunity-card__section">
          <h4>限制</h4>
          <p
            v-if="item.limitations === undefined"
            data-value-state="unknown"
          >
            未覆盖
          </p>
          <p v-else-if="item.limitations.length === 0">
            API 明确返回空集合
          </p>
          <ul v-else class="research-opportunity-card__list">
            <li
              v-for="limitation in item.limitations"
              :key="limitation"
            >
              {{ limitation }}
            </li>
          </ul>
        </section>

        <section class="research-opportunity-card__section">
          <h4>建议下一步</h4>
          <p
            v-if="item.recommended_next_steps === undefined"
            data-value-state="unknown"
          >
            未覆盖
          </p>
          <p v-else-if="item.recommended_next_steps.length === 0">
            API 明确返回空集合
          </p>
          <ol v-else class="research-opportunity-card__list research-opportunity-card__list--ordered">
            <li
              v-for="step in item.recommended_next_steps"
              :key="step"
            >
              {{ step }}
            </li>
          </ol>
        </section>
      </article>
    </div>
  </section>
</template>

<style scoped>
.research-opportunity-board {
  display: grid;
  gap: var(--space-4);
}

.research-opportunity-board__grid {
  display: grid;
  gap: var(--space-4);
}

.research-opportunity-card {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.research-opportunity-card__topline,
.research-opportunity-card__headline,
.research-opportunity-card__section {
  display: grid;
  gap: var(--space-2);
}

.research-opportunity-card__topline {
  grid-template-columns: minmax(0, max-content) auto;
  align-items: start;
  justify-content: space-between;
}

.research-opportunity-card__headline h3,
.research-opportunity-card__headline p,
.research-opportunity-card__section h4,
.research-opportunity-card__section p,
.research-opportunity-card__section ul,
.research-opportunity-card__section ol,
.research-opportunity-card__section li,
.research-opportunity-card__meta,
.research-opportunity-card__meta dd {
  margin: 0;
}

.research-opportunity-card__headline h3 {
  color: var(--color-text-strong);
  font-size: var(--text-xl-size);
  overflow-wrap: anywhere;
}

.research-opportunity-card__summary {
  color: var(--color-text);
  overflow-wrap: anywhere;
}

.research-opportunity-card__meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-3);
}

.research-opportunity-card__meta > div,
.research-opportunity-card__section,
.research-opportunity-card__estimates li {
  min-width: 0;
}

.research-opportunity-card__meta > div {
  display: grid;
  gap: var(--space-1);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.research-opportunity-card__meta dt,
.research-opportunity-card__section h4 {
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  font-weight: 700;
  text-transform: uppercase;
}

.research-opportunity-card__meta dd,
.research-opportunity-card__section p,
.research-opportunity-card__section li,
.research-opportunity-card__section code,
.research-opportunity-card__section small,
.research-opportunity-card__section span,
.research-opportunity-card__estimates strong {
  font-size: var(--text-sm-size);
}

.research-opportunity-card__meta dd,
.research-opportunity-card__section p,
.research-opportunity-card__section li {
  color: var(--color-text-strong);
  overflow-wrap: anywhere;
}

.research-opportunity-card__meta code,
.research-opportunity-card__section code {
  font-family: var(--font-data);
  font-size: inherit;
  font-variant-numeric: tabular-nums;
}

.research-opportunity-card__meta dd[data-value-state="missing"],
.research-opportunity-card__meta dd[data-value-state="unknown"],
.research-opportunity-card__section p[data-value-state="missing"],
.research-opportunity-card__section p[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-family: var(--font-ui);
  font-style: italic;
}

.research-opportunity-card__estimates,
.research-opportunity-card__work-ids,
.research-opportunity-card__list {
  display: grid;
  gap: var(--space-2);
  padding: 0;
  list-style: none;
}

.research-opportunity-card__estimates li {
  display: grid;
  gap: var(--space-1);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.research-opportunity-card__estimates strong {
  color: var(--color-text-strong);
  overflow-wrap: anywhere;
}

.research-opportunity-card__estimates span {
  color: var(--color-positive);
  font-family: var(--font-data);
  font-variant-numeric: tabular-nums;
  font-weight: 750;
}

.research-opportunity-card__estimates small {
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.research-opportunity-card__work-ids code {
  color: var(--color-text-strong);
  overflow-wrap: anywhere;
}

.research-opportunity-card__list--ordered {
  list-style: decimal;
  padding-inline-start: var(--space-5);
}

@media (min-width: 768px) {
  .research-opportunity-card__meta {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (min-width: 1440px) {
  .research-opportunity-board__grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
