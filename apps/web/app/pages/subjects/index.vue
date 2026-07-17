<script setup lang="ts">
import {
  catalogDataValue,
} from "~/utils/catalogPresentation"
import { pageQueryFromRoute } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"
import { presentDataValue } from "~/utils/dataValue"

definePageMeta({
  title: "学科",
})

useHead({
  title: "学科",
  meta: [
    {
      name: "description",
      content: "浏览版本化 JCR Category biomedical Subject taxonomy。",
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
  "subjects-catalog",
  () =>
    settleCatalogRequest(() =>
      client.listSubjects(pageQueryFromRoute(route.query)),
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
        Versioned JCR Category registry
      </p>
      <h1>学科</h1>
      <p class="page-lede">
        学科来自已授权、版本化的 biomedical JCR Category registry；页面不根据名称或摘要做启发式归并。
      </p>
    </header>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/subjects"
      @retry="refresh"
    />

    <template v-if="readyData">
      <section
        class="subject-list-scope"
        data-intelligence-module="subject-list"
        aria-labelledby="subject-list-heading"
      >
        <div class="section-heading">
          <div>
            <p class="section-kicker">
              当前发布范围
            </p>
            <h2 id="subject-list-heading">
              Subject taxonomy
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
        title="当前目录暂无学科"
        message="API 已成功返回，但当前 generation 中没有可见 Subject。"
      />
      <section v-else class="subject-grid" aria-label="学科列表">
        <article v-for="subject in readyData.items" :key="subject.id">
          <p>JCR {{ subject.jcr_metric_year }}</p>
          <h2>
            <NuxtLink :to="`/subjects/${subject.slug}`">
              {{ subject.name }}
            </NuxtLink>
          </h2>
          <p :data-value-state="catalogDataValue(subject.description).state">
            {{ presentDataValue(catalogDataValue(subject.description)).text }}
          </p>
          <dl>
            <div>
              <dt>论文</dt>
              <dd :data-value-state="catalogDataValue(subject.paper_count).state">
                {{ presentDataValue(catalogDataValue(subject.paper_count)).text }}
              </dd>
            </div>
            <div>
              <dt>期刊</dt>
              <dd :data-value-state="catalogDataValue(subject.journal_count).state">
                {{ presentDataValue(catalogDataValue(subject.journal_count)).text }}
              </dd>
            </div>
          </dl>
          <p class="subject-grid__taxonomy">
            taxonomy version {{ subject.taxonomy_version }}
          </p>
        </article>
      </section>
      <CatalogPagination :pagination="readyData.pagination" />
    </template>
  </div>
</template>

<style scoped>
.subject-list-scope {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.subject-grid {
  display: grid;
  gap: var(--space-3);
}

.subject-grid article {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.subject-grid h2,
.subject-grid p,
.subject-grid dl,
.subject-grid dd {
  margin: 0;
}

.subject-grid h2 {
  color: var(--color-text-strong);
  font-size: var(--text-xl-size);
}

.subject-grid h2 a {
  display: inline-flex;
  min-height: 44px;
  align-items: center;
  color: inherit;
}

.subject-grid > article > p {
  color: var(--color-text-muted);
}

.subject-grid dl {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-2);
}

.subject-grid dl > div {
  display: grid;
  gap: var(--space-1);
  padding: var(--space-2);
  background: var(--color-surface-subtle);
}

.subject-grid dt {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}

.subject-grid dd {
  color: var(--color-text-strong);
  font-family: var(--font-data);
  font-variant-numeric: tabular-nums;
  font-weight: 800;
}

.subject-grid__taxonomy {
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
  .subject-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1440px) {
  .subject-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
