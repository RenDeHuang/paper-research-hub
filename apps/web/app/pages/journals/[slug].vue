<script setup lang="ts">
import {
  catalogDataValue,
} from "~/utils/catalogPresentation"
import {
  CatalogQueryError,
  pageQueryFromRoute,
} from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"
import { presentDataValue } from "~/utils/dataValue"

definePageMeta({
  title: "期刊",
})

const route = useRoute()
const client = useCatalogApi()

function journalSlug() {
  if (typeof route.params.slug !== "string" || route.params.slug.length === 0) {
    throw new CatalogQueryError("slug", "Journal slug 无效")
  }
  return route.params.slug
}

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "journal-detail",
  () =>
    settleCatalogRequest(() =>
      client.getJournal(
        journalSlug(),
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
    ? `${readyData.value.title} · 期刊`
    : "期刊详情",
}))
</script>

<template>
  <div class="portal-page">
    <div class="page-actions">
      <NuxtLink class="button button--secondary" to="/journals">
        返回期刊
      </NuxtLink>
    </div>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      not-found-action-to="/journals"
      @retry="refresh"
    />

    <template v-if="readyData">
      <header class="page-header">
        <p class="page-eyebrow">
          Curated journal
        </p>
        <h1>{{ readyData.title }}</h1>
        <p v-if="readyData.aliases?.length" class="page-lede">
          别名：{{ readyData.aliases.join("、") }}
        </p>
      </header>

      <section class="journal-identity" aria-labelledby="journal-identity-heading">
        <h2 id="journal-identity-heading">
          期刊身份与 JCR 指标
        </h2>
        <dl class="detail-list">
          <div>
            <dt>Publisher</dt>
            <dd :data-value-state="catalogDataValue(readyData.publisher).state">
              {{ presentDataValue(catalogDataValue(readyData.publisher)).text }}
            </dd>
          </div>
          <div>
            <dt>ISSN-L</dt>
            <dd :data-value-state="catalogDataValue(readyData.issn_l).state">
              {{ presentDataValue(catalogDataValue(readyData.issn_l)).text }}
            </dd>
          </div>
          <div>
            <dt>ISSN</dt>
            <dd :data-value-state="readyData.issns === undefined ? 'unknown' : 'known'">
              {{ readyData.issns?.join("、") ?? "未覆盖" }}
            </dd>
          </div>
          <div>
            <dt>eISSN</dt>
            <dd :data-value-state="catalogDataValue(readyData.eissn).state">
              {{ presentDataValue(catalogDataValue(readyData.eissn)).text }}
            </dd>
          </div>
          <div>
            <dt>JCR 指标年份</dt>
            <dd>{{ readyData.jcr_metric_year }}</dd>
          </div>
          <div>
            <dt>JIF</dt>
            <dd :data-value-state="catalogDataValue(readyData.jif).state">
              {{ presentDataValue(catalogDataValue(readyData.jif)).text }}
            </dd>
          </div>
          <div>
            <dt>taxonomy version</dt>
            <dd>{{ readyData.taxonomy_version }}</dd>
          </div>
          <div>
            <dt>当前论文量</dt>
            <dd :data-value-state="catalogDataValue(readyData.paper_count).state">
              {{ presentDataValue(catalogDataValue(readyData.paper_count)).text }}
            </dd>
          </div>
        </dl>
      </section>

      <JournalCurationEvidence
        :categories="readyData.categories ?? []"
        :curation="readyData.curation"
      />

      <section class="journal-section" aria-labelledby="journal-papers-heading">
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              当前 generation
            </p>
            <h2 id="journal-papers-heading">
              最近论文
            </h2>
          </div>
        </div>
        <IntelligenceModuleMeta :analysis="readyData.recent_papers.analysis" />
        <DataState
          v-if="readyData.recent_papers.items.length === 0"
          state="empty"
          title="当前期刊没有最近论文"
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

      <EditorialPatternTable :patterns="readyData.editorial_patterns" />

      <section class="journal-section" aria-labelledby="journal-gaps-heading">
        <h2 id="journal-gaps-heading">
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

.journal-identity,
.journal-section {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.journal-identity h2,
.journal-section h2,
.journal-section p {
  margin: 0;
  color: var(--color-text-strong);
}

.evidence-gap-list {
  display: grid;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
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
</style>
