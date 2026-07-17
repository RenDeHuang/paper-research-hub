<script setup lang="ts">
import type { DataValue } from "~/utils/dataValue"
import {
  catalogDataValue,
  formatCatalogDate,
  percentageDataValue,
} from "~/utils/catalogPresentation"
import { settleCatalogRequest } from "~/utils/catalogResult"
import { presentDataValue } from "~/utils/dataValue"

useHead({
  title: "医学生物学研究情报",
  meta: [
    {
      name: "description",
      content: "聚合 JCR Q1 或 JIF 不低于 10 的医学与生物学期刊论文、引用和趋势证据。",
    },
  ],
})

const client = useCatalogApi()
const {
  data: result,
  status,
  refresh,
} = await useAsyncData("home-catalog", () =>
  settleCatalogRequest(() => client.getHome()),
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

  const windowValue = catalogDataValue(
    readyData.value.coverage.analysis.window_days,
  )
  const generatedAt = catalogDataValue(
    readyData.value.coverage.analysis.generated_at,
  )

  return {
    coverage: percentageDataValue(
      readyData.value.coverage.analysis.coverage_ratio,
    ),
    dataRange:
      windowValue.state === "known"
        ? {
            state: "known",
            value: `${windowValue.value} 天统计窗口`,
          }
        : windowValue,
    updatedAt:
      generatedAt.state === "known"
        ? {
            state: "known",
            value: formatCatalogDate(generatedAt.value),
          }
        : generatedAt,
  }
})

</script>

<template>
  <div class="home-page portal-page">
    <section class="home-page__intro" aria-labelledby="home-heading">
      <p class="page-eyebrow">
        medpaperhub
      </p>
      <h1 id="home-heading">
        医学生物学研究情报，从新论文到可验证趋势
      </h1>
      <p class="page-lede">
        查看重点学科和期刊最近发表了什么、哪些论文引用增长更快，以及哪些研究方向仍有明确证据缺口。
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
      <section class="home-scope" aria-label="当前目录范围">
        <dl class="detail-list">
          <div>
            <dt>Catalog generation</dt>
            <dd>{{ readyData.catalog_generation }}</dd>
          </div>
          <div>
            <dt>JCR 指标年份</dt>
            <dd>{{ readyData.scope.jcr_metric_year }}</dd>
          </div>
          <div>
            <dt>taxonomy version</dt>
            <dd>{{ readyData.scope.taxonomy_version }}</dd>
          </div>
        </dl>
      </section>

      <BiomedicalDailyBrief :brief="readyData.latest_papers" />

      <div class="home-page__analysis-grid">
        <SubjectMomentumGrid :momentum="readyData.subject_momentum" />
        <CitationMomentumList :momentum="readyData.citation_momentum" />
      </div>

      <JournalActivityList :activity="readyData.active_journals" />

      <EntityMomentumGrid :momentum="readyData.entity_momentum" />

      <section
        class="intelligence-module home-opportunities"
        data-intelligence-module="research-opportunities"
        aria-labelledby="home-opportunities-heading"
      >
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              结构化证据触发
            </p>
            <h2 id="home-opportunities-heading">
              研究机会
            </h2>
          </div>
        <NuxtLink class="button button--secondary" to="/opportunities">
          查看全部机会
        </NuxtLink>
      </div>
      <ResearchOpportunityBoard
        :collection="readyData.research_opportunities"
      />
      </section>

      <section
        class="intelligence-module coverage-panel"
        data-intelligence-module="coverage"
        aria-labelledby="coverage-heading"
      >
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              可信度边界
            </p>
            <h2 id="coverage-heading">
              数据覆盖
            </h2>
          </div>
        </div>

        <IntelligenceModuleMeta :analysis="readyData.coverage.analysis" />

        <dl class="coverage-panel__grid">
          <div>
            <dt>引用覆盖率</dt>
            <dd :data-value-state="percentageDataValue(readyData.coverage.citation_coverage_ratio).state">
              {{ presentDataValue(percentageDataValue(readyData.coverage.citation_coverage_ratio)).text }}
            </dd>
          </div>
          <div>
            <dt>MeSH 覆盖率</dt>
            <dd :data-value-state="percentageDataValue(readyData.coverage.mesh_coverage_ratio).state">
              {{ presentDataValue(percentageDataValue(readyData.coverage.mesh_coverage_ratio)).text }}
            </dd>
          </div>
          <div>
            <dt>Publication Type 覆盖率</dt>
            <dd :data-value-state="percentageDataValue(readyData.coverage.publication_type_coverage_ratio).state">
              {{ presentDataValue(percentageDataValue(readyData.coverage.publication_type_coverage_ratio)).text }}
            </dd>
          </div>
          <div>
            <dt>JCR 指标年份</dt>
            <dd>{{ readyData.coverage.jcr_metric_year }}</dd>
          </div>
          <div>
            <dt>taxonomy version</dt>
            <dd>{{ readyData.coverage.taxonomy_version }}</dd>
          </div>
          <div>
            <dt>全局证据缺口</dt>
            <dd :data-value-state="readyData.evidence_gaps.length > 0 ? 'missing' : 'known'">
              {{
                readyData.evidence_gaps.length > 0
                  ? readyData.evidence_gaps.join("、")
                  : "API 明确返回空集合"
              }}
            </dd>
          </div>
        </dl>
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

.home-scope,
.intelligence-module {
  display: grid;
  gap: var(--space-4);
}

.home-scope {
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.home-page__analysis-grid {
  display: grid;
  gap: var(--space-8);
}

.home-opportunities__grid,
.coverage-panel__grid {
  display: grid;
  gap: var(--space-3);
}

.home-opportunities article,
.coverage-panel__grid > div {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.home-opportunities h3,
.home-opportunities p,
.coverage-panel__grid,
.coverage-panel__grid dd {
  margin: 0;
}

.coverage-panel__grid dt {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}

.home-opportunities h3 {
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
}

.home-opportunities__topline {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
  justify-content: space-between;
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
}

.home-opportunities__topline span {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm);
  background: var(--color-caution-bg);
  color: var(--color-caution);
  font-weight: 800;
}

.home-opportunities p {
  color: var(--color-text-muted);
}

.coverage-panel__grid dd {
  color: var(--color-text-strong);
  font-family: var(--font-data);
  font-variant-numeric: tabular-nums;
  font-weight: 800;
  overflow-wrap: anywhere;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted) !important;
  font-family: var(--font-ui) !important;
  font-style: italic;
}

@media (min-width: 768px) {
  .home-opportunities__grid,
  .coverage-panel__grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1440px) {
  .home-page__analysis-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .coverage-panel__grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
