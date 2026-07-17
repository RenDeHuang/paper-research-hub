<script setup lang="ts">
import type { DistributionBucket } from "~/types/biomedical"
import {
  catalogDataValue,
  percentageDataValue,
} from "~/utils/catalogPresentation"
import {
  CatalogQueryError,
  pageQueryFromRoute,
} from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"
import { presentDataValue } from "~/utils/dataValue"

definePageMeta({
  title: "学科",
})

const route = useRoute()
const client = useCatalogApi()

function subjectSlug() {
  if (typeof route.params.slug !== "string" || route.params.slug.length === 0) {
    throw new CatalogQueryError("slug", "Subject slug 无效")
  }
  return route.params.slug
}

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "subject-detail",
  () =>
    settleCatalogRequest(() =>
      client.getSubject(
        subjectSlug(),
        pageQueryFromRoute(route.query),
      ),
    ),
  {
    watch: [() => route.params.slug, () => route.query.cursor],
  },
)
const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)

useHead(() => ({
  title: readyData.value
    ? `${readyData.value.name} · 学科`
    : "学科详情",
}))

function distributionValue(item: DistributionBucket) {
  return percentageDataValue(item.ratio)
}
</script>

<template>
  <div class="portal-page">
    <div class="page-actions">
      <NuxtLink class="button button--secondary" to="/subjects">
        返回学科
      </NuxtLink>
    </div>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      not-found-action-to="/subjects"
      @retry="refresh"
    />

    <template v-if="readyData">
      <header class="page-header">
        <p class="page-eyebrow">
          Biomedical Subject
        </p>
        <h1>{{ readyData.name }}</h1>
        <p
          class="page-lede"
          :data-value-state="catalogDataValue(readyData.description).state"
        >
          {{ presentDataValue(catalogDataValue(readyData.description)).text }}
        </p>
      </header>

      <section class="subject-scope" aria-label="学科版本与范围">
        <dl class="detail-list">
          <div>
            <dt>JCR 指标年份</dt>
            <dd>{{ readyData.jcr_metric_year }}</dd>
          </div>
          <div>
            <dt>taxonomy version</dt>
            <dd>{{ readyData.taxonomy_version }}</dd>
          </div>
          <div>
            <dt>论文量</dt>
            <dd :data-value-state="catalogDataValue(readyData.paper_count).state">
              {{ presentDataValue(catalogDataValue(readyData.paper_count)).text }}
            </dd>
          </div>
          <div>
            <dt>期刊量</dt>
            <dd :data-value-state="catalogDataValue(readyData.journal_count).state">
              {{ presentDataValue(catalogDataValue(readyData.journal_count)).text }}
            </dd>
          </div>
        </dl>
      </section>

      <section class="subject-section" aria-labelledby="subject-papers-heading">
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              当前 generation
            </p>
            <h2 id="subject-papers-heading">
              最近论文
            </h2>
          </div>
        </div>
        <IntelligenceModuleMeta :analysis="readyData.recent_papers.analysis" />
        <DataState
          v-if="readyData.recent_papers.items.length === 0"
          state="empty"
          title="当前学科没有最近论文"
          message="API 已成功返回空集合；页面不会跨 endpoint 补取论文。"
        />
        <div v-else class="catalog-list">
          <CatalogPaperCard
            v-for="paper in readyData.recent_papers.items"
            :key="paper.id"
            :paper="paper"
          />
        </div>
        <CatalogPagination :pagination="readyData.recent_papers.pagination" />
      </section>

      <JournalActivityList :activity="readyData.active_journals" />

      <div class="subject-distributions">
        <section class="subject-section" aria-labelledby="mesh-distribution-heading">
          <h2 id="mesh-distribution-heading">
            MeSH 分布
          </h2>
          <IntelligenceModuleMeta :analysis="readyData.mesh_distribution.analysis" />
          <DataState
            v-if="readyData.mesh_distribution.items.length === 0"
            state="empty"
            title="当前没有 MeSH 分布"
            message="API 已成功返回空集合。"
          />
          <ol v-else class="distribution-list">
            <li v-for="item in readyData.mesh_distribution.items" :key="item.label">
              <span>{{ item.label }}</span>
              <strong :data-value-state="distributionValue(item).state">
                {{ presentDataValue(distributionValue(item)).text }}
              </strong>
              <small :data-value-state="catalogDataValue(item.count).state">
                样本 {{ presentDataValue(catalogDataValue(item.count)).text }}
              </small>
            </li>
          </ol>
        </section>

        <section class="subject-section" aria-labelledby="publication-type-heading">
          <h2 id="publication-type-heading">
            Publication Type 分布
          </h2>
          <IntelligenceModuleMeta :analysis="readyData.publication_type_distribution.analysis" />
          <DataState
            v-if="readyData.publication_type_distribution.items.length === 0"
            state="empty"
            title="当前没有 Publication Type 分布"
            message="API 已成功返回空集合。"
          />
          <ol v-else class="distribution-list">
            <li
              v-for="item in readyData.publication_type_distribution.items"
              :key="item.label"
            >
              <span>{{ item.label }}</span>
              <strong :data-value-state="distributionValue(item).state">
                {{ presentDataValue(distributionValue(item)).text }}
              </strong>
              <small :data-value-state="catalogDataValue(item.count).state">
                样本 {{ presentDataValue(catalogDataValue(item.count)).text }}
              </small>
            </li>
          </ol>
        </section>
      </div>

      <section class="subject-section" aria-labelledby="subject-trends-heading">
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              预先声明模型
            </p>
            <h2 id="subject-trends-heading">
              趋势估计与不确定性
            </h2>
          </div>
        </div>
        <IntelligenceModuleMeta :analysis="readyData.trend_estimates.analysis" />
        <DataState
          v-if="readyData.trend_estimates.items.length === 0"
          state="insufficient"
          title="当前没有可发布的趋势估计"
          message="样本量不足或缺少历史基线时，API 不生成肯定结论。"
        />
        <div v-else class="trend-estimate-grid">
          <article v-for="item in readyData.trend_estimates.items" :key="item.label">
            <h3>{{ item.label ?? "趋势估计" }}</h3>
            <p class="metric-card__model">
              模型：{{ item.model_family }}
            </p>
            <p :data-value-state="catalogDataValue(item.estimate).state">
              {{
                catalogDataValue(item.estimate).state === "known"
                  ? `${presentDataValue(catalogDataValue(item.estimate)).text}×`
                  : presentDataValue(catalogDataValue(item.estimate)).text
              }}
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
                <dd>{{ presentDataValue(catalogDataValue(item.independent_journal_count)).text }}</dd>
              </div>
              <div>
                <dt>独立团队</dt>
                <dd>{{ presentDataValue(catalogDataValue(item.independent_team_count)).text }}</dd>
              </div>
            </dl>
          </article>
        </div>
      </section>

      <section class="subject-section" aria-labelledby="subject-gaps-heading">
        <h2 id="subject-gaps-heading">
          证据缺口
        </h2>
        <p
          v-if="readyData.evidence_gaps.length === 0"
          data-value-state="known"
        >
          API 明确返回空集合
        </p>
        <ul v-else class="evidence-gap-list">
          <li v-for="gap in readyData.evidence_gaps" :key="gap">
            {{ gap }}
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.page-actions {
  display: flex;
}

.subject-scope,
.subject-section {
  display: grid;
  gap: var(--space-4);
}

.subject-scope,
.subject-section {
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.subject-section > h2 {
  margin: 0;
  color: var(--color-text-strong);
}

.subject-distributions {
  display: grid;
  gap: var(--space-4);
}

.distribution-list,
.evidence-gap-list {
  display: grid;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.distribution-list li {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--space-1) var(--space-3);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.distribution-list span {
  color: var(--color-text-strong);
  font-weight: 750;
}

.distribution-list strong {
  color: var(--color-positive);
  font-family: var(--font-data);
  font-variant-numeric: tabular-nums;
}

.distribution-list small {
  grid-column: 1 / -1;
  color: var(--color-text-muted);
}

.trend-estimate-grid {
  display: grid;
  gap: var(--space-3);
}

.trend-estimate-grid article {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.trend-estimate-grid h3,
.trend-estimate-grid p,
.trend-estimate-grid dl,
.trend-estimate-grid dd {
  margin: 0;
}

.trend-estimate-grid h3 {
  color: var(--color-text-strong);
}

.trend-estimate-grid > article > p {
  color: var(--color-positive);
  font-family: var(--font-data);
  font-size: var(--text-2xl-size);
  font-weight: 800;
}

.trend-estimate-grid dl {
  display: grid;
  gap: var(--space-2);
}

.trend-estimate-grid dl > div {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--space-3);
}

.trend-estimate-grid dt {
  color: var(--color-text-muted);
}

.trend-estimate-grid dd {
  color: var(--color-text-strong);
  font-variant-numeric: tabular-nums;
}

.evidence-gap-list li {
  padding: var(--space-3);
  border-left: 4px solid var(--color-caution);
  background: var(--color-caution-bg);
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted) !important;
  font-family: var(--font-ui) !important;
  font-style: italic;
}

@media (min-width: 768px) {
  .subject-distributions {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .trend-estimate-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
