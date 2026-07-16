<script setup lang="ts">
import type { DataValue } from "~/utils/dataValue"
import type {
  CatalogValue,
} from "~/types/catalog"
import {
  catalogDataValue,
  formatCatalogDate,
  missingSignalLabels,
  opportunityMatrixStatus,
  opportunityStatusPresentation,
} from "~/utils/catalogPresentation"
import { opportunityQueryFromRoute } from "~/utils/catalogQuery"
import { settleCatalogRequest } from "~/utils/catalogResult"

definePageMeta({
  title: "研究机会",
})

useHead({
  title: "研究机会 · Paper Research Hub",
  meta: [
    {
      name: "description",
      content: "查看真实 API 生成、带证据边界的研究机会。",
    },
  ],
})

const route = useRoute()
const router = useRouter()
const client = useCatalogApi()
const selectedStatus = ref(
  typeof route.query.status === "string" ? route.query.status : "",
)

watch(
  () => route.query.status,
  (value) => {
    selectedStatus.value = typeof value === "string" ? value : ""
  },
)

const {
  data: result,
  status,
  refresh,
} = await useAsyncData(
  "opportunities-catalog",
  () =>
    settleCatalogRequest(() =>
      client.listResearchOpportunities(
        opportunityQueryFromRoute(route.query),
      ),
    ),
  {
    watch: [() => route.fullPath],
  },
)
const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)

function percentageValue(
  value: CatalogValue<number> | undefined,
): DataValue<number> {
  const normalized = catalogDataValue(value)
  if (normalized.state !== "known") {
    return normalized
  }
  return {
    state: "known",
    value: Math.round(normalized.value * 1000) / 10,
  }
}

const matrixPoints = computed(() =>
  (readyData.value?.items ?? []).map((item) => {
    const explicitMissing = item.missing_signals
      ?? missingSignalLabels(item.ranking?.missing_signals)
    return {
      id: item.id,
      label: item.title,
      missingSignals:
        explicitMissing.length > 0 ? explicitMissing : [],
      status: opportunityMatrixStatus(item.status),
      x: percentageValue(item.competition_density),
      y: percentageValue(item.growth_score),
    }
  }),
)

async function applyStatus() {
  await router.push({
    path: "/opportunities",
    query:
      selectedStatus.value.length > 0
        ? { status: selectedStatus.value }
        : {},
  })
}
</script>

<template>
  <div class="portal-page">
    <header class="page-header">
      <p class="page-eyebrow">
        证据支持的方向判断
      </p>
      <h1>研究机会</h1>
      <p class="page-lede">
        坐标、推荐状态、证据 ID 与缺失信号均来自 API；未知与缺失不会被估算或填补。
      </p>
    </header>

    <form class="compact-filter" @submit.prevent="applyStatus">
      <div>
        <label for="opportunity-status">推荐状态</label>
        <select
          id="opportunity-status"
          v-model="selectedStatus"
          name="status"
        >
          <option value="">全部状态</option>
          <option value="worth_pursuing">值得做</option>
          <option value="proceed_with_caution">谨慎做</option>
          <option value="not_recommended_now">当前不建议做</option>
          <option value="insufficient_evidence">证据不足</option>
        </select>
      </div>
      <button class="button button--primary" type="submit">
        应用筛选
      </button>
    </form>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      invalid-action-to="/opportunities"
      @retry="refresh"
    />

    <template v-if="readyData">
      <section class="analysis-meta" aria-label="研究机会生成信息">
        <dl class="detail-list">
          <div>
            <dt>生成时间</dt>
            <dd>{{ formatCatalogDate(readyData.generated_at) }}</dd>
          </div>
          <div>
            <dt>公式版本</dt>
            <dd>{{ readyData.formula_version }}</dd>
          </div>
        </dl>
      </section>

      <DataState
        v-if="readyData.items.length === 0"
        state="empty"
        title="当前没有研究机会"
        message="API 已成功返回，但当前筛选条件下没有研究机会记录。"
        action-label="清除筛选"
        action-to="/opportunities"
      />

      <template v-else>
        <OpportunityMatrix
          :points="matrixPoints"
          title="增长信号与竞争密度"
          summary="仅绘制 API 明确给出且落在 0–100 范围内的增长分与竞争密度。"
          x-axis-label="竞争密度"
          y-axis-label="增长信号"
        />

        <section class="opportunity-list" aria-labelledby="opportunity-list-heading">
          <h2 id="opportunity-list-heading">
            方向详情
          </h2>
          <article
            v-for="item in readyData.items"
            :key="item.id"
            class="opportunity-card"
          >
            <div class="opportunity-card__heading">
              <span
                class="status-badge"
                :class="{
                  'status-badge--positive': item.status === 'worth_pursuing',
                  'status-badge--caution': item.status === 'proceed_with_caution'
                    || item.status === 'insufficient_evidence',
                  'status-badge--negative': item.status === 'not_recommended_now',
                }"
              >
                {{ opportunityStatusPresentation(item.status) }}
              </span>
              <span>{{ item.formula_version }}</span>
            </div>
            <h3>{{ item.title }}</h3>
            <p
              class="opportunity-card__summary"
              :data-value-state="item.summary === undefined ? 'unknown' : 'known'"
            >
              {{ item.summary ?? "摘要字段未覆盖" }}
            </p>

            <div class="opportunity-card__evidence">
              <EvidenceBar
                label="增长信号"
                :value="percentageValue(item.growth_score)"
                :max="100"
                unit="%"
              />
              <EvidenceBar
                label="竞争密度"
                :value="percentageValue(item.competition_density)"
                :max="100"
                unit="%"
              />
              <EvidenceBar
                label="数据可用性"
                :value="percentageValue(item.data_availability)"
                :max="100"
                unit="%"
              />
              <EvidenceBar
                label="可复现性"
                :value="percentageValue(item.reproducibility)"
                :max="100"
                unit="%"
              />
            </div>

            <div>
              <h4>证据论文</h4>
              <ul class="evidence-links">
                <li v-for="paperID in item.evidence_ids" :key="paperID">
                  <NuxtLink :to="`/papers/${paperID}`">
                    {{ paperID }}
                  </NuxtLink>
                </li>
              </ul>
            </div>

            <div>
              <h4>缺失信号</h4>
              <p
                v-if="(item.missing_signals ?? missingSignalLabels(item.ranking?.missing_signals)).length === 0"
              >
                API 明确返回空集合
              </p>
              <ul v-else>
                <li
                  v-for="signal in item.missing_signals ?? missingSignalLabels(item.ranking?.missing_signals)"
                  :key="signal"
                >
                  {{ signal }}
                </li>
              </ul>
            </div>

            <div>
              <h4>建议下一步</h4>
              <DataState
                v-if="item.recommended_next_steps === undefined"
                state="unknown"
                title="建议字段未覆盖"
                message="当前 API 响应没有 recommended_next_steps，页面不会补写建议。"
              />
              <ul v-else>
                <li v-for="step in item.recommended_next_steps" :key="step">
                  {{ step }}
                </li>
              </ul>
            </div>
          </article>
        </section>
        <CatalogPagination :pagination="readyData.pagination" />
      </template>
    </template>
  </div>
</template>

<style scoped>
.analysis-meta {
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.opportunity-list {
  display: grid;
  gap: var(--space-4);
}

.opportunity-list > h2 {
  margin: 0;
}

.opportunity-card {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.opportunity-card__heading {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
  justify-content: space-between;
  color: var(--color-text-muted);
  font-family: var(--font-data);
  font-size: var(--text-xs-size);
}

.status-badge {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  padding: 2px var(--space-2);
  border-radius: 999px;
  background: var(--color-neutral-bg);
  color: var(--color-text-strong);
  font-family: var(--font-ui);
  font-weight: 800;
}

.status-badge--positive {
  background: var(--color-positive-bg);
  color: var(--color-positive);
}

.status-badge--caution {
  background: var(--color-caution-bg);
  color: var(--color-caution);
}

.status-badge--negative {
  background: var(--color-negative-bg);
  color: var(--color-negative);
}

.opportunity-card h3,
.opportunity-card h4,
.opportunity-card p {
  margin: 0;
}

.opportunity-card h3 {
  color: var(--color-text-strong);
  font-size: var(--text-xl-size);
}

.opportunity-card h4 {
  margin-bottom: var(--space-2);
  color: var(--color-text-strong);
}

.opportunity-card__summary {
  max-width: 75ch;
  line-height: 1.65;
}

[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-style: italic;
}

.opportunity-card__evidence {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-4);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.opportunity-card ul {
  display: grid;
  gap: var(--space-2);
  margin: 0;
  padding-left: var(--space-5);
}

.evidence-links a {
  display: inline-flex;
  min-height: 44px;
  align-items: center;
  overflow-wrap: anywhere;
}

@media (min-width: 768px) {
  .opportunity-card__evidence {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
