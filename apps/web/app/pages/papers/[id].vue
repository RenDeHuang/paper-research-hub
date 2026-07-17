<script setup lang="ts">
import type { CurationPayload } from "~/types/catalog"
import {
  catalogDataValue,
  paperSources,
} from "~/utils/catalogPresentation"
import { CatalogQueryError } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"
import { presentDataValue } from "~/utils/dataValue"

definePageMeta({
  title: "论文详情",
})

const route = useRoute()
const client = useCatalogApi()

function paperID() {
  if (typeof route.params.id !== "string" || route.params.id.length === 0) {
    throw new CatalogQueryError("id", "论文 ID 无效")
  }
  return route.params.id
}

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "paper-detail",
  () => settleCatalogRequest(() => client.getPaper(paperID())),
  {
    watch: [() => route.params.id],
  },
)

const paper = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)
const abstract = computed(() =>
  presentDataValue(
    catalogDataValue(paper.value?.abstract, "摘要字段未覆盖"),
  ),
)
const sources = computed(() =>
  paperSources(paper.value?.source_provenance),
)
const curation = computed<
  | { state: "known"; value: CurationPayload }
  | { state: "missing" | "unknown" }
>(() => {
  const value = paper.value?.curation
  if (value === undefined) {
    return { state: "unknown" }
  }
  if (
    typeof value === "object"
    && value !== null
    && "state" in value
  ) {
    return value
  }
  return { state: "known", value }
})

useHead(() => ({
  title: paper.value
    ? paper.value.title
    : "论文详情",
}))
</script>

<template>
  <div class="portal-page paper-detail-page">
    <div class="page-actions">
      <NuxtLink class="button button--secondary" to="/papers">
        返回论文目录
      </NuxtLink>
    </div>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      not-found-action-to="/papers"
      @retry="refresh"
    />

    <template v-if="paper">
      <CatalogPaperCard :paper="paper" />

      <section class="detail-section" aria-labelledby="abstract-heading">
        <p class="section-kicker">
          内容
        </p>
        <h2 id="abstract-heading">
          摘要
        </h2>
        <p :data-value-state="abstract.state">
          {{ abstract.text }}
        </p>
      </section>

      <section class="detail-section" aria-labelledby="identity-heading">
        <p class="section-kicker">
          标识与来源
        </p>
        <h2 id="identity-heading">
          可追溯信息
        </h2>
        <dl class="detail-list">
          <div>
            <dt>论文 ID</dt>
            <dd>{{ paper.id }}</dd>
          </div>
          <div>
            <dt>规范键</dt>
            <dd :data-value-state="paper.canonical_key ? 'known' : 'unknown'">
              {{ paper.canonical_key ?? "未覆盖" }}
            </dd>
          </div>
          <div>
            <dt>来源集合</dt>
            <dd :data-value-state="sources.state">
              {{ presentDataValue(sources).text }}
            </dd>
          </div>
        </dl>
        <DataState
          v-if="paper.source_provenance === undefined"
          state="unknown"
          title="来源明细未覆盖"
          message="当前 API 响应没有 source_provenance 明细，页面不会推断来源时间或策略版本。"
        />
        <ul v-else class="provenance-list">
          <li
            v-for="source in paper.source_provenance"
            :key="typeof source === 'string' ? source : `${source.source}:${source.source_record_id}`"
          >
            <template v-if="typeof source === 'string'">
              {{ source }}
            </template>
            <template v-else>
              <strong>{{ source.source }}</strong>
              <span>记录：{{ source.source_record_id }}</span>
              <span v-if="source.source_time">来源时间：{{ source.source_time }}</span>
              <span v-if="source.normalization_policy_version">
                归一化策略：{{ source.normalization_policy_version }}
              </span>
            </template>
          </li>
        </ul>
      </section>

      <section class="detail-section" aria-labelledby="curation-heading">
        <p class="section-kicker">
          策略判断
        </p>
        <h2 id="curation-heading">
          收录评估
        </h2>
        <DataState
          v-if="curation.state !== 'known'"
          :state="curation.state"
          title="评估信息不可用"
          :message="curation.state === 'missing'
            ? '该记录预期存在评估，但当前数据缺失。'
            : '当前来源未覆盖评估信息。'"
        />
        <dl v-else class="detail-list">
          <div>
            <dt>决策</dt>
            <dd>{{ curation.value.decision }}</dd>
          </div>
          <div>
            <dt>策略</dt>
            <dd>
              {{ curation.value.policy_name }} v{{ curation.value.policy_version }}
            </dd>
          </div>
          <div>
            <dt>指标年份</dt>
            <dd>{{ curation.value.metric_year }}</dd>
          </div>
          <div>
            <dt>评估时间</dt>
            <dd>{{ curation.value.assessed_at }}</dd>
          </div>
        </dl>
      </section>
    </template>
  </div>
</template>

<style scoped>
.paper-detail-page {
  max-width: 960px;
}

.page-actions {
  display: flex;
}

.detail-section {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.detail-section h2,
.detail-section p {
  margin: 0;
}

.detail-section > p:not(.section-kicker) {
  max-width: 75ch;
  line-height: 1.7;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-style: italic;
}

.provenance-list {
  display: grid;
  gap: var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}

.provenance-list li {
  display: grid;
  gap: var(--space-1);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
  overflow-wrap: anywhere;
}

.provenance-list span {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}
</style>
