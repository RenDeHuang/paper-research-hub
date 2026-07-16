<script setup lang="ts">
import {
  presentDataValue,
  type DataValue,
} from "~/utils/dataValue"

type StatusTone = "caution" | "info" | "negative" | "neutral" | "positive"

interface LabelledValue {
  label: string
  value: DataValue
}

interface PaperTag {
  category?: string
  label: string
}

defineProps<{
  authors?: DataValue<string[]>
  detailLabel?: string
  detailTo?: string
  evidence?: LabelledValue[]
  metrics?: LabelledValue[]
  publishedAt?: DataValue<string>
  sourceType?: DataValue<string>
  status?: {
    description?: string
    label: string
    tone: StatusTone
  }
  summary?: DataValue<string>
  tags?: PaperTag[]
  title: string
  venue?: DataValue<string>
}>()
</script>

<template>
  <article class="paper-card">
    <div v-if="status || sourceType" class="paper-card__topline">
      <span
        v-if="status"
        class="status-badge"
        :class="`status-badge--${status.tone}`"
        :title="status.description"
      >
        <span class="status-badge__shape" aria-hidden="true" />
        {{ status.label }}
      </span>
      <span
        v-if="sourceType"
        class="paper-card__source"
        :data-value-state="presentDataValue(sourceType).state"
      >
        {{ presentDataValue(sourceType).text }}
      </span>
    </div>

    <h2 class="paper-card__title">
      <NuxtLink v-if="detailTo" :to="detailTo">
        {{ title }}
      </NuxtLink>
      <template v-else>
        {{ title }}
      </template>
    </h2>

    <dl v-if="authors || venue || publishedAt" class="paper-card__byline">
      <div v-if="authors">
        <dt>作者</dt>
        <dd :data-value-state="presentDataValue(authors).state">
          {{ presentDataValue(authors).text }}
        </dd>
      </div>
      <div v-if="venue">
        <dt>Venue</dt>
        <dd :data-value-state="presentDataValue(venue).state">
          {{ presentDataValue(venue).text }}
        </dd>
      </div>
      <div v-if="publishedAt">
        <dt>发布日期</dt>
        <dd :data-value-state="presentDataValue(publishedAt).state">
          {{ presentDataValue(publishedAt).text }}
        </dd>
      </div>
    </dl>

    <p
      v-if="summary"
      class="paper-card__summary"
      :data-value-state="presentDataValue(summary).state"
    >
      {{ presentDataValue(summary).text }}
    </p>

    <ul v-if="tags?.length" class="paper-card__tags" aria-label="论文分类">
      <li v-for="tag in tags" :key="`${tag.category ?? 'tag'}:${tag.label}`">
        <span v-if="tag.category" class="paper-card__tag-category">
          {{ tag.category }}
        </span>
        {{ tag.label }}
      </li>
    </ul>

    <dl v-if="evidence?.length" class="paper-card__evidence">
      <div v-for="item in evidence" :key="item.label">
        <dt>{{ item.label }}</dt>
        <dd
          class="paper-card__evidence-value"
          :data-value-state="presentDataValue(item.value).state"
          :data-value-kind="presentDataValue(item.value).kind"
        >
          {{ presentDataValue(item.value).text }}
        </dd>
      </div>
    </dl>

    <dl v-if="metrics?.length" class="paper-card__metrics">
      <div v-for="item in metrics" :key="item.label">
        <dt>{{ item.label }}</dt>
        <dd
          :data-value-state="presentDataValue(item.value).state"
          :data-value-kind="presentDataValue(item.value).kind"
        >
          {{ presentDataValue(item.value).text }}
        </dd>
      </div>
    </dl>

    <NuxtLink v-if="detailTo" class="paper-card__detail" :to="detailTo">
      {{ detailLabel ?? "查看详情" }}
    </NuxtLink>
  </article>
</template>

<style scoped>
.paper-card {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.paper-card:hover {
  border-color: var(--color-border-control);
}

.paper-card__topline {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
}

.status-badge,
.paper-card__source {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  gap: var(--space-2);
  padding: 2px var(--space-2);
  border-radius: 999px;
  font-size: var(--text-xs-size);
  font-weight: 700;
  line-height: var(--text-xs-line);
}

.status-badge {
  background: var(--warm-100);
  color: var(--color-text-strong);
}

.status-badge__shape {
  width: 8px;
  height: 8px;
  border: 2px solid currentColor;
  border-radius: 50%;
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

.status-badge--negative .status-badge__shape {
  border-radius: var(--radius-sm);
}

.status-badge--info {
  background: var(--color-info-bg);
  color: var(--color-info);
}

.paper-card__source {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
}

.paper-card__title {
  margin: 0;
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
  line-height: var(--text-lg-line);
  overflow-wrap: anywhere;
}

.paper-card__title a {
  color: inherit;
  text-decoration-thickness: 1px;
  text-underline-offset: 3px;
}

.paper-card__byline,
.paper-card__evidence,
.paper-card__metrics {
  display: grid;
  gap: var(--space-2);
  margin: 0;
}

.paper-card__byline > div,
.paper-card__evidence > div,
.paper-card__metrics > div {
  display: grid;
  grid-template-columns: minmax(88px, auto) minmax(0, 1fr);
  gap: var(--space-3);
}

dt {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

dd {
  min-width: 0;
  margin: 0;
  color: var(--color-text);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
  overflow-wrap: anywhere;
}

[data-value-kind="number"] {
  font-variant-numeric: tabular-nums;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-style: italic;
}

.paper-card__summary {
  max-width: 68ch;
  margin: 0;
  color: var(--color-text);
  line-height: 1.65;
}

.paper-card__tags {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.paper-card__tags li {
  padding: var(--space-1) var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
  color: var(--color-text);
  font-size: var(--text-xs-size);
  line-height: var(--text-xs-line);
}

.paper-card__tag-category {
  color: var(--color-text-muted);
}

.paper-card__metrics {
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
}

.paper-card__metrics > div {
  display: grid;
  grid-template-columns: 1fr;
  gap: var(--space-1);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.paper-card__metrics dd {
  color: var(--color-text-strong);
  font-family: var(--font-data);
  font-weight: 700;
}

.paper-card__detail {
  width: fit-content;
  min-height: 44px;
  align-content: center;
  color: var(--color-link);
  font-weight: 700;
}

@media (min-width: 768px) {
  .paper-card {
    padding: var(--space-5);
  }
}
</style>
