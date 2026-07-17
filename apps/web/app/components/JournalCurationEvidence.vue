<script setup lang="ts">
import type {
  JournalCuration,
  JournalMetricCategory,
} from "~/types/biomedical"

defineProps<{
  categories: JournalMetricCategory[]
  curation: JournalCuration
}>()
</script>

<template>
  <section class="curation-evidence" aria-labelledby="curation-evidence-heading">
    <div class="section-heading">
      <div>
        <p class="section-kicker">
          可审计准入
        </p>
        <h2 id="curation-evidence-heading">
          命中的准入规则
        </h2>
      </div>
      <span class="curation-evidence__decision">
        {{ curation.decision === "accepted" ? "已纳入" : curation.decision }}
      </span>
    </div>

    <dl class="detail-list">
      <div>
        <dt>JCR 指标年份</dt>
        <dd>{{ curation.metric_year }}</dd>
      </div>
      <div>
        <dt>策略</dt>
        <dd>{{ curation.policy_name }} v{{ curation.policy_version }}</dd>
      </div>
      <div>
        <dt>评估时间</dt>
        <dd>{{ formatCatalogDate(curation.assessed_at) }}</dd>
      </div>
    </dl>

    <div class="curation-evidence__rules">
      <h3>命中规则</h3>
      <DataState
        v-if="curation.matched_rules.length === 0"
        state="missing"
        title="准入规则缺失"
        message="accepted assessment 必须给出命中规则；页面不会推测准入原因。"
      />
      <ul v-else>
        <li v-for="rule in curation.matched_rules" :key="rule">
          <code>{{ rule }}</code>
        </li>
      </ul>
    </div>

    <div class="curation-evidence__categories">
      <h3>全部 JCR Category / Quartile</h3>
      <DataState
        v-if="categories.length === 0"
        state="missing"
        title="JCR Category 缺失"
        message="当前响应没有 Category / Quartile 证据。"
      />
      <ul v-else>
        <li v-for="category in categories" :key="`${category.name}:${category.quartile}`">
          {{ category.name }} · {{ category.quartile }}
        </li>
      </ul>
    </div>

    <div class="curation-evidence__source">
      <h3>准入证据</h3>
      <DataState
        v-if="curation.evidence.length === 0"
        state="missing"
        title="准入证据缺失"
        message="当前 assessment 没有可展示的结构化证据。"
      />
      <dl v-else>
        <div v-for="item in curation.evidence" :key="`${item.label}:${item.value}`">
          <dt>{{ item.label }}</dt>
          <dd>{{ item.value }}</dd>
        </div>
      </dl>
    </div>
  </section>
</template>

<style scoped>
.curation-evidence {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-left: 4px solid var(--color-positive);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.curation-evidence__decision {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm);
  background: var(--color-positive-bg);
  color: var(--color-positive);
  font-size: var(--text-xs-size);
  font-weight: 800;
}

h3 {
  margin: 0 0 var(--space-2);
  color: var(--color-text-strong);
  font-size: var(--text-base-size);
}

ul {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

li {
  min-height: 28px;
  align-content: center;
  padding: var(--space-1) var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
  color: var(--color-text-strong);
  font-size: var(--text-sm-size);
}

.curation-evidence__source > dl {
  display: grid;
  gap: var(--space-2);
  margin: 0;
}

.curation-evidence__source > dl > div {
  display: grid;
  grid-template-columns: minmax(120px, auto) minmax(0, 1fr);
  gap: var(--space-3);
  padding: var(--space-2) 0;
  border-bottom: 1px solid var(--color-border);
}

.curation-evidence__source > dl > div:last-child {
  border-bottom: 0;
}

.curation-evidence__source dt,
.curation-evidence__source dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.curation-evidence__source dt {
  color: var(--color-text-muted);
}

.curation-evidence__source dd {
  color: var(--color-text-strong);
  font-weight: 750;
}
</style>
