<script setup lang="ts">
import type { PublicationUpdateItem } from "~/types/biomedical"
import { catalogDataValue } from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

const props = defineProps<{
  item: PublicationUpdateItem
}>()

const eventLabels = {
  accepted: "已接收",
  ahead_of_print: "在线优先",
  electronic_published: "电子正式发表",
  print_published: "印刷正式发表",
} as const

const citationCount = computed(() =>
  presentDataValue(catalogDataValue(props.item.paper.citation_count)),
)
const journal = computed(() => props.item.paper.journal)
const publicationTypes = computed(() => {
  const values = props.item.paper.publication_types ?? []
  return values.length > 0
    ? values.join(" / ")
    : "类型未标注"
})
</script>

<template>
  <article
    class="publication-event-card"
    :data-event-kind="item.event.kind"
  >
    <div class="publication-event-card__meta">
      <span>{{ eventLabels[item.event.kind] }}</span>
      <time :datetime="item.event.date">
        {{ item.event.date }}
      </time>
    </div>

    <h3>
      <NuxtLink :to="`/papers/${item.paper.id}`">
        {{ item.paper.title }}
      </NuxtLink>
    </h3>

    <div class="publication-event-card__details">
      <NuxtLink
        v-if="journal"
        :to="`/journals/${journal.slug}`"
      >
        {{ journal.title }}
      </NuxtLink>
      <span v-else>期刊未标注</span>
      <span>{{ publicationTypes }}</span>
      <span
        :data-value-state="citationCount.state"
        :data-value-kind="citationCount.kind"
      >
        引用 {{ citationCount.text }}
      </span>
    </div>
  </article>
</template>

<style scoped>
.publication-event-card {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
  padding: var(--space-4) 0;
  border-bottom: 1px solid var(--color-border);
}

.publication-event-card:first-child {
  padding-top: 0;
}

.publication-event-card:last-child {
  padding-bottom: 0;
  border-bottom: 0;
}

.publication-event-card__meta,
.publication-event-card__details {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-3);
  align-items: center;
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  line-height: var(--text-xs-line);
}

.publication-event-card__meta span {
  padding: 2px var(--space-2);
  border-radius: var(--radius-sm);
  background: var(--color-info-bg);
  color: var(--color-info);
  font-weight: 800;
}

.publication-event-card__meta time {
  font-family: var(--font-data);
  font-variant-numeric: tabular-nums;
}

h3 {
  margin: 0;
  color: var(--color-text-strong);
  font-size: var(--text-base-size);
  line-height: 1.45;
}

h3 a {
  display: inline-flex;
  min-width: 44px;
  min-height: 44px;
  align-items: center;
  color: inherit;
  text-decoration: none;
}

h3 a:hover {
  color: var(--color-link);
  text-decoration: underline;
}

.publication-event-card__details a {
  min-width: 44px;
  min-height: 44px;
  align-content: center;
  font-weight: 700;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-style: italic;
}
</style>
