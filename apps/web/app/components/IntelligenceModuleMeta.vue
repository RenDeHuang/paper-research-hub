<script setup lang="ts">
import type { AnalysisMetadata } from "~/types/biomedical"
import {
  catalogDataValue,
  formatCatalogDate,
  missingSignalLabels,
  percentageDataValue,
} from "~/utils/catalogPresentation"
import {
  presentDataValue,
  type DataValue,
} from "~/utils/dataValue"

const props = defineProps<{
  analysis: AnalysisMetadata
}>()

const windowValue = computed<DataValue<number>>(() => {
  const value = catalogDataValue(props.analysis.window_days)
  if (value.state !== "known") {
    return value
  }
  return {
    state: "known",
    suffix: " 天",
    value: value.value,
  }
})
const generatedAt = computed<DataValue<string>>(() => {
  const value = catalogDataValue(props.analysis.generated_at)
  if (value.state !== "known") {
    return value
  }
  return {
    state: "known",
    value: formatCatalogDate(value.value),
  }
})
const analysisRunID = computed(() =>
  catalogDataValue(props.analysis.analysis_run_id),
)
const analysisType = computed(() =>
  catalogDataValue(props.analysis.analysis_type),
)
const cohortRevision = computed(() =>
  catalogDataValue(props.analysis.cohort_revision),
)
const recentWindowDays = computed<DataValue<number>>(() => {
  const value = catalogDataValue(props.analysis.recent_window_days)
  if (value.state !== "known") {
    return value
  }
  return {
    state: "known",
    suffix: " 天",
    value: value.value,
  }
})
const baselineWindowDays = computed<DataValue<number>>(() => {
  const value = catalogDataValue(props.analysis.baseline_window_days)
  if (value.state !== "known") {
    return value
  }
  return {
    state: "known",
    suffix: " 天",
    value: value.value,
  }
})
const sampleSize = computed(() =>
  catalogDataValue(props.analysis.sample_size),
)
const coverage = computed(() =>
  percentageDataValue(props.analysis.coverage_ratio),
)
const sources = computed(() =>
  catalogDataValue(props.analysis.sources),
)
const formulaVersion = computed(() =>
  catalogDataValue(
    props.analysis.formula_version,
    "公式版本字段未覆盖",
  ),
)
const missingSignals = computed(() =>
  missingSignalLabels(props.analysis.missing_signals),
)
</script>

<template>
  <dl class="intelligence-meta" aria-label="分析范围与覆盖">
    <div>
      <dt>分析 run ID</dt>
      <dd
        data-analysis-run-id
        :data-value-state="analysisRunID.state"
      >
        {{ presentDataValue(analysisRunID).text }}
      </dd>
    </div>
    <div>
      <dt>分析类型</dt>
      <dd
        data-analysis-type
        :data-value-state="analysisType.state"
      >
        {{ presentDataValue(analysisType).text }}
      </dd>
    </div>
    <div>
      <dt>cohort revision</dt>
      <dd
        data-cohort-revision
        :data-value-state="cohortRevision.state"
      >
        {{ presentDataValue(cohortRevision).text }}
      </dd>
    </div>
    <div>
      <dt>统计窗口</dt>
      <dd
        data-window
        :data-value-state="windowValue.state"
      >
        {{ presentDataValue(windowValue).text }}
      </dd>
    </div>
    <div>
      <dt>最近窗口</dt>
      <dd
        data-recent-window-days
        :data-value-state="recentWindowDays.state"
      >
        {{ presentDataValue(recentWindowDays).text }}
      </dd>
    </div>
    <div>
      <dt>基线窗口</dt>
      <dd
        data-baseline-window-days
        :data-value-state="baselineWindowDays.state"
      >
        {{ presentDataValue(baselineWindowDays).text }}
      </dd>
    </div>
    <div>
      <dt>生成 / 更新时间</dt>
      <dd
        data-generated-at
        :data-value-state="generatedAt.state"
      >
        {{ presentDataValue(generatedAt).text }}
      </dd>
    </div>
    <div>
      <dt>样本量</dt>
      <dd
        data-sample-size
        :data-value-state="sampleSize.state"
      >
        {{ presentDataValue(sampleSize).text }}
      </dd>
    </div>
    <div>
      <dt>覆盖率</dt>
      <dd
        data-coverage
        :data-value-state="coverage.state"
      >
        {{ presentDataValue(coverage).text }}
      </dd>
    </div>
    <div>
      <dt>数据来源</dt>
      <dd :data-value-state="sources.state">
        {{ presentDataValue(sources).text }}
      </dd>
    </div>
    <div>
      <dt>公式版本</dt>
      <dd
        data-formula-version
        :data-value-state="formulaVersion.state"
      >
        {{ presentDataValue(formulaVersion).text }}
      </dd>
    </div>
    <div>
      <dt>缺失状态</dt>
      <dd
        data-missing-state
        :data-value-state="missingSignals.length > 0 ? 'missing' : 'known'"
      >
        {{
          missingSignals.length > 0
            ? missingSignals.join("、")
            : "无已声明缺失信号"
        }}
      </dd>
    </div>
  </dl>
</template>

<style scoped>
.intelligence-meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1px;
  margin: 0;
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-border);
}

.intelligence-meta > div {
  display: grid;
  gap: var(--space-1);
  min-width: 0;
  padding: var(--space-3);
  background: var(--color-surface-subtle);
}

dt,
dd {
  margin: 0;
  font-size: var(--text-xs-size);
  line-height: var(--text-xs-line);
}

dt {
  color: var(--color-text-muted);
  font-weight: 700;
}

dd {
  color: var(--color-text-strong);
  font-variant-numeric: tabular-nums;
  font-weight: 750;
  overflow-wrap: anywhere;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-family: var(--font-ui);
  font-style: italic;
}

@media (min-width: 768px) {
  .intelligence-meta {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
