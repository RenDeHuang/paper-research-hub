<script setup lang="ts">
import type { TrendItem } from "~/types/catalog"
import {
  missingSignalLabels,
  trendItemIdentity,
} from "~/utils/catalogPresentation"

defineProps<{
  items: TrendItem[]
  title: string
}>()

function scoreText(score: number) {
  return `${Math.round(score * 1000) / 10}%`
}
</script>

<template>
  <section class="trend-ranking" :aria-labelledby="`${title}-heading`">
    <h2 :id="`${title}-heading`">
      {{ title }}
    </h2>
    <DataState
      v-if="items.length === 0"
      state="empty"
      title="当前窗口没有排名结果"
      message="API 已返回成功，但当前时间窗口内没有可排名实体。"
    />
    <ol v-else>
      <li v-for="item in items" :key="`${title}:${item.rank}:${trendItemIdentity(item).label}`">
        <span class="trend-ranking__rank">#{{ item.rank }}</span>
        <div class="trend-ranking__body">
          <p class="trend-ranking__type">
            {{ trendItemIdentity(item).type }}
          </p>
          <h3>
            <NuxtLink
              v-if="trendItemIdentity(item).to"
              :to="trendItemIdentity(item).to"
            >
              {{ trendItemIdentity(item).label }}
            </NuxtLink>
            <template v-else>
              {{ trendItemIdentity(item).label }}
            </template>
          </h3>
          <p>
            趋势分 {{ scoreText(item.score) }}
            <template v-if="item.rank_change !== undefined">
              · 排名变化 {{ item.rank_change > 0 ? "+" : "" }}{{ item.rank_change }}
            </template>
          </p>
          <p
            v-if="missingSignalLabels(item.ranking.missing_signals).length > 0"
            class="trend-ranking__missing"
          >
            缺失信号：{{ missingSignalLabels(item.ranking.missing_signals).join("、") }}
          </p>
        </div>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.trend-ranking {
  display: grid;
  gap: var(--space-4);
}

h2 {
  margin: 0;
  color: var(--color-text-strong);
  font-size: var(--text-xl-size);
}

ol {
  display: grid;
  gap: var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}

li {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: var(--space-4);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.trend-ranking__rank {
  color: var(--color-primary);
  font-family: var(--font-data);
  font-size: var(--text-xl-size);
  font-weight: 800;
}

.trend-ranking__body {
  min-width: 0;
}

.trend-ranking__body > * {
  margin: 0;
}

.trend-ranking__type {
  color: var(--color-info);
  font-size: var(--text-xs-size);
  font-weight: 800;
}

h3 {
  margin-top: var(--space-1) !important;
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
  overflow-wrap: anywhere;
}

h3 a {
  display: inline-flex;
  min-height: 44px;
  align-items: center;
  color: inherit;
}

h3 + p,
.trend-ranking__missing {
  margin-top: var(--space-2) !important;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}

.trend-ranking__missing {
  color: var(--color-caution);
}
</style>
