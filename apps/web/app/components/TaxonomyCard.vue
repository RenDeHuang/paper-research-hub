<script setup lang="ts">
import type { TaxonomyItem } from "~/types/catalog"
import { catalogDataValue } from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

const props = defineProps<{
  item: TaxonomyItem
  kind: "methods" | "topics"
}>()

const description = computed(() =>
  presentDataValue(catalogDataValue(props.item.description)),
)
</script>

<template>
  <article class="taxonomy-card">
    <p class="taxonomy-card__kind">
      {{ kind === "topics" ? "Topic" : "Method" }}
    </p>
    <h2>
      <NuxtLink :to="`/${kind}/${item.slug}`">
        {{ item.name }}
      </NuxtLink>
    </h2>
    <p
      class="taxonomy-card__description"
      :data-value-state="description.state"
    >
      {{ description.text }}
    </p>
    <p class="taxonomy-card__count">
      {{ item.paper_count }} 篇论文
    </p>
  </article>
</template>

<style scoped>
.taxonomy-card {
  display: grid;
  gap: var(--space-3);
  min-width: 0;
  padding: var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.taxonomy-card:hover {
  border-color: var(--color-border-control);
}

.taxonomy-card__kind,
.taxonomy-card__description,
.taxonomy-card__count {
  margin: 0;
}

.taxonomy-card__kind {
  color: var(--color-info);
  font-size: var(--text-xs-size);
  font-weight: 800;
  letter-spacing: 0.06em;
}

h2 {
  margin: 0;
  font-size: var(--text-xl-size);
  line-height: var(--text-xl-line);
}

h2 a {
  display: inline-flex;
  min-height: 44px;
  align-items: center;
  color: var(--color-text-strong);
}

.taxonomy-card__description {
  color: var(--color-text);
  line-height: 1.65;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-style: italic;
}

.taxonomy-card__count {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  font-variant-numeric: tabular-nums;
  font-weight: 700;
}
</style>
