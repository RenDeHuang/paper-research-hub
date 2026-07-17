<script setup lang="ts">
import type { PaperDetail } from "~/types/catalog"
import type { components } from "~/types/openapi.generated"
import {
  catalogDataValue,
  formatCatalogDate,
} from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

type CitationMetricValue =
  | components["schemas"]["CitationPercentileCatalogValue"]
  | components["schemas"]["CitationVelocityCatalogValue"]

const props = defineProps<{
  paper: PaperDetail
}>()

const citationSource = computed(() =>
  catalogDataValue(props.paper.citation_source),
)
const citationVelocity = computed(() =>
  presentCitationMetric(props.paper.citation_velocity, " 次/天"),
)
const citationPercentile = computed(() =>
  presentCitationMetric(props.paper.citation_percentile, "%"),
)
const citationSnapshots = computed(() => props.paper.citation_snapshots)
const snapshots = computed(() =>
  citationSnapshots.value.state === "known"
    ? citationSnapshots.value.value
    : undefined,
)
const unavailableSnapshotsState = computed<"missing" | "unknown">(() =>
  citationSnapshots.value.state === "missing" ? "missing" : "unknown",
)
const citationAnalysisEvidence = computed(
  () => props.paper.citation_analysis_evidence,
)
const analysisEvidence = computed(() =>
  citationAnalysisEvidence.value?.state === "known"
    ? citationAnalysisEvidence.value.value
    : undefined,
)
const analysisEvidenceState = computed(() =>
  citationAnalysisEvidence.value?.state ?? "unknown",
)
const unavailableAnalysisEvidenceState = computed<"missing" | "unknown">(() =>
  analysisEvidenceState.value === "missing" ? "missing" : "unknown",
)
const analysisEvidenceMessage = computed(() =>
  analysisEvidenceState.value === "missing"
    ? "本次发布预期存在分析运行证据，但正式字段标记为缺失。"
    : "当前正式 OpenAPI 响应未覆盖 citation_analysis_evidence；页面不会从引用快照推断分析 run、速度窗口或 cohort。",
)

function presentCitationMetric(
  value: CitationMetricValue,
  suffix: string,
) {
  if (value.state === "known") {
    return {
      state: value.state,
      text: `${value.value}${suffix}`,
    }
  }
  if (value.state === "insufficient_evidence") {
    return {
      state: value.state,
      text: `证据不足：${value.reason}`,
    }
  }
  return {
    state: value.state,
    text: value.state === "missing" ? "缺失" : "未覆盖",
  }
}
</script>

<template>
  <section class="detail-section citation-evidence" aria-labelledby="citation-evidence-heading">
    <p class="section-kicker">
      引用指标
    </p>
    <h2 id="citation-evidence-heading">
      引用证据
    </h2>

    <dl class="detail-list citation-evidence__metrics">
      <div>
        <dt>引用来源</dt>
        <dd :data-value-state="citationSource.state">
          {{ presentDataValue(citationSource).text }}
        </dd>
      </div>
      <div>
        <dt>引用速度</dt>
        <dd
          data-citation-velocity
          :data-value-state="citationVelocity.state"
        >
          {{ citationVelocity.text }}
        </dd>
      </div>
      <div>
        <dt>同 cohort 百分位</dt>
        <dd
          data-citation-percentile
          :data-value-state="citationPercentile.state"
        >
          {{ citationPercentile.text }}
        </dd>
      </div>
    </dl>

    <div class="citation-evidence__block">
      <h3>来源快照</h3>
      <DataState
        v-if="snapshots === undefined"
        :state="unavailableSnapshotsState"
        title="引用快照不可用"
        :message="citationSnapshots.state === 'missing'
          ? '指定引用来源预期存在快照，但本次发布标记为缺失。'
          : '当前引用来源未覆盖可展示的快照。'"
      />
      <ul v-else class="citation-snapshot-list">
        <li
          v-for="snapshot in snapshots"
          :key="`${snapshot.source}:${snapshot.source_record_id}:${snapshot.observed_at}`"
        >
          <dl class="detail-list">
            <div>
              <dt>来源</dt>
              <dd>{{ snapshot.source }}</dd>
            </div>
            <div>
              <dt>引用数</dt>
              <dd>{{ snapshot.count }}</dd>
            </div>
            <div>
              <dt>观测时间</dt>
              <dd>{{ formatCatalogDate(snapshot.observed_at) }}</dd>
            </div>
            <div>
              <dt>抓取时间</dt>
              <dd>{{ formatCatalogDate(snapshot.retrieved_at) }}</dd>
            </div>
            <div>
              <dt>定义版本</dt>
              <dd>{{ snapshot.definition_version }}</dd>
            </div>
            <div>
              <dt>数据集版本</dt>
              <dd>{{ snapshot.dataset_version }}</dd>
            </div>
            <div>
              <dt>source record ID</dt>
              <dd>{{ snapshot.source_record_id }}</dd>
            </div>
            <div>
              <dt>ingestion job ID</dt>
              <dd>{{ snapshot.ingestion_job_id }}</dd>
            </div>
            <div>
              <dt>覆盖率</dt>
              <dd>{{ snapshot.coverage * 100 }}%</dd>
            </div>
          </dl>
        </li>
      </ul>
    </div>

    <div
      class="citation-evidence__block"
      data-citation-analysis-evidence
      :data-state="analysisEvidenceState"
    >
      <h3>分析运行证据</h3>
      <DataState
        v-if="analysisEvidence === undefined"
        :state="unavailableAnalysisEvidenceState"
        title="分析运行证据不可用"
        :message="analysisEvidenceMessage"
      />
      <template v-else>
        <dl class="detail-list">
          <div>
            <dt>分析 run ID</dt>
            <dd>{{ analysisEvidence.analysis_run_id }}</dd>
          </div>
          <div>
            <dt>来源</dt>
            <dd>{{ analysisEvidence.source }}</dd>
          </div>
          <div>
            <dt>as of</dt>
            <dd>{{ formatCatalogDate(analysisEvidence.as_of) }}</dd>
          </div>
          <div>
            <dt>生成时间</dt>
            <dd>{{ formatCatalogDate(analysisEvidence.generated_at) }}</dd>
          </div>
          <div>
            <dt>公式版本</dt>
            <dd>{{ analysisEvidence.formula_version }}</dd>
          </div>
          <div>
            <dt>source revision</dt>
            <dd>{{ analysisEvidence.source_revision }}</dd>
          </div>
        </dl>

        <div class="citation-evidence__analysis-grid">
          <section aria-labelledby="velocity-evidence-heading">
            <h4 id="velocity-evidence-heading">速度证据</h4>
            <DataState
              v-if="analysisEvidence.velocity.state === 'insufficient_evidence'"
              state="insufficient"
              title="引用速度证据不足"
              :message="analysisEvidence.velocity.reason"
            />
            <dl v-else class="detail-list">
              <div>
                <dt>速度窗口</dt>
                <dd>{{ analysisEvidence.velocity.window_days }} 天</dd>
              </div>
              <div>
                <dt>真实间隔</dt>
                <dd>{{ analysisEvidence.velocity.elapsed_days }} 天</dd>
              </div>
              <div>
                <dt>起点快照</dt>
                <dd>{{ analysisEvidence.velocity.baseline_snapshot_id }}</dd>
              </div>
              <div>
                <dt>终点快照</dt>
                <dd>{{ analysisEvidence.velocity.current_snapshot_id }}</dd>
              </div>
            </dl>
          </section>

          <section aria-labelledby="percentile-evidence-heading">
            <h4 id="percentile-evidence-heading">百分位 cohort 证据</h4>
            <DataState
              v-if="analysisEvidence.percentile.state === 'insufficient_evidence'"
              state="insufficient"
              title="引用百分位证据不足"
              :message="analysisEvidence.percentile.reason"
            />
            <dl v-else class="detail-list">
              <div>
                <dt>cohort key</dt>
                <dd>{{ analysisEvidence.percentile.cohort_key }}</dd>
              </div>
              <div>
                <dt>cohort 大小</dt>
                <dd>{{ analysisEvidence.percentile.cohort_size }}</dd>
              </div>
              <div>
                <dt>最低 cohort 大小</dt>
                <dd>{{ analysisEvidence.percentile.minimum_cohort_size }}</dd>
              </div>
              <div>
                <dt>midrank</dt>
                <dd>{{ analysisEvidence.percentile.midrank }}</dd>
              </div>
              <div>
                <dt>Subject version ID</dt>
                <dd>{{ analysisEvidence.percentile.subject_version_id }}</dd>
              </div>
              <div>
                <dt>Subject ID</dt>
                <dd>{{ analysisEvidence.percentile.subject_id }}</dd>
              </div>
              <div>
                <dt>发表年份</dt>
                <dd>{{ analysisEvidence.percentile.publication_year }}</dd>
              </div>
              <div>
                <dt>Publication Type ID</dt>
                <dd>{{ analysisEvidence.percentile.publication_type_id }}</dd>
              </div>
              <div>
                <dt>引用快照 ID</dt>
                <dd>{{ analysisEvidence.percentile.citation_snapshot_id }}</dd>
              </div>
              <div>
                <dt>支持 Work IDs</dt>
                <dd>{{ analysisEvidence.percentile.supporting_work_ids.join("、") }}</dd>
              </div>
            </dl>
          </section>
        </div>
      </template>
    </div>
  </section>
</template>

<style scoped>
.citation-evidence {
  gap: var(--space-4);
}

.citation-evidence__block {
  display: grid;
  gap: var(--space-3);
}

.citation-evidence h3,
.citation-evidence h4 {
  margin: 0;
  color: var(--color-text-strong);
}

.citation-evidence h3 {
  font-size: var(--text-base-size);
}

.citation-evidence h4 {
  font-size: var(--text-sm-size);
}

.citation-snapshot-list {
  display: grid;
  gap: var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}

.citation-snapshot-list li {
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.citation-evidence__analysis-grid {
  display: grid;
  gap: var(--space-3);
}

.citation-evidence__analysis-grid > section {
  display: grid;
  gap: var(--space-3);
  min-width: 0;
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.detail-list dd {
  overflow-wrap: anywhere;
}

[data-value-state="missing"],
[data-value-state="unknown"],
[data-value-state="insufficient_evidence"] {
  color: var(--color-text-muted);
  font-style: italic;
}

@media (min-width: 768px) {
  .citation-evidence__analysis-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
