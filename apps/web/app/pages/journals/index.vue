<script setup lang="ts">
import {
  catalogDataValue,
} from "~/utils/catalogPresentation"
import { pageQueryFromRoute } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"
import { presentDataValue } from "~/utils/dataValue"

definePageMeta({
  title: "期刊",
})

useHead({
  title: "期刊",
  meta: [
    {
      name: "description",
      content: "浏览当前 Catalog generation 中通过全部 JCR Q1 准入规则的期刊。",
    },
  ],
})

const route = useRoute()
const client = useCatalogApi()
const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "journals-catalog",
  () =>
    settleCatalogRequest(() =>
      client.listJournals(pageQueryFromRoute(route.query)),
    ),
  {
    watch: [() => route.fullPath],
  },
)
const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)
</script>

<template>
  <div class="portal-page">
    <header class="page-header">
      <p class="page-eyebrow">
        Curated journals
      </p>
      <h1>期刊</h1>
      <p class="page-lede">
        仅展示当前 Catalog generation 已接受的期刊；JCR Category、Quartile、JIF 和指标年份保持可审计。
      </p>
    </header>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/journals"
      @retry="refresh"
    />

    <template v-if="readyData">
      <section
        class="journal-list-scope"
        data-intelligence-module="journal-list"
        aria-labelledby="journal-list-heading"
      >
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              当前发布范围
            </p>
            <h2 id="journal-list-heading">
              Accepted journal catalog
            </h2>
          </div>
        </div>
        <dl class="detail-list">
          <div>
            <dt>Catalog generation</dt>
            <dd>{{ readyData.catalog_generation }}</dd>
          </div>
          <div>
            <dt>JCR 指标年份</dt>
            <dd>{{ readyData.jcr_metric_year }}</dd>
          </div>
          <div>
            <dt>taxonomy version</dt>
            <dd>{{ readyData.taxonomy_version }}</dd>
          </div>
        </dl>
        <IntelligenceModuleMeta :analysis="readyData.analysis" />
      </section>

      <DataState
        v-if="readyData.items.length === 0"
        state="empty"
        title="当前目录暂无期刊"
        message="API 已成功返回，但当前 generation 中没有通过准入门槛的期刊。"
      />
      <section v-else class="journal-grid" aria-label="期刊列表">
        <article v-for="journal in readyData.items" :key="journal.id">
          <div class="journal-grid__topline">
            <span>JCR {{ journal.jcr_metric_year }}</span>
            <span :data-value-state="catalogDataValue(journal.jif).state">
              JIF {{ presentDataValue(catalogDataValue(journal.jif)).text }}
            </span>
          </div>
          <h2>
            <NuxtLink :to="`/journals/${journal.slug}`">
              {{ journal.title }}
            </NuxtLink>
          </h2>
          <p :data-value-state="catalogDataValue(journal.publisher).state">
            Publisher：{{ presentDataValue(catalogDataValue(journal.publisher)).text }}
          </p>
          <ul v-if="journal.categories?.length">
            <li v-for="category in journal.categories" :key="`${category.name}:${category.quartile}`">
              {{ category.name }} · {{ category.quartile }}
            </li>
          </ul>
          <DataState
            v-else
            state="missing"
            title="Category / Quartile 缺失"
            message="当前响应没有可展示的 JCR 分类证据。"
          />
          <p class="journal-grid__taxonomy">
            taxonomy version {{ journal.taxonomy_version ?? readyData.taxonomy_version }}
          </p>
        </article>
      </section>
      <CatalogPagination :pagination="readyData.pagination" />
    </template>
  </div>
</template>

<style scoped>
.journal-list-scope {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.journal-grid {
  display: grid;
  gap: var(--space-3);
}

.journal-grid article {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.journal-grid h2,
.journal-grid p,
.journal-grid ul {
  margin: 0;
}

.journal-grid h2 {
  color: var(--color-text-strong);
  font-size: var(--text-xl-size);
}

.journal-grid h2 a {
  display: inline-flex;
  min-height: 44px;
  align-items: center;
  color: inherit;
}

.journal-grid__topline {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
  justify-content: space-between;
  color: var(--color-text-muted);
  font-family: var(--font-data);
  font-size: var(--text-xs-size);
}

.journal-grid > article > p {
  color: var(--color-text-muted);
}

.journal-grid ul {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  padding: 0;
  list-style: none;
}

.journal-grid li {
  padding: var(--space-1) var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
  font-size: var(--text-xs-size);
}

.journal-grid__taxonomy {
  font-family: var(--font-data);
  font-size: var(--text-xs-size);
  overflow-wrap: anywhere;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted) !important;
  font-family: var(--font-ui) !important;
  font-style: italic;
}

@media (min-width: 768px) {
  .journal-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1440px) {
  .journal-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
