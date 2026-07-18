<script setup lang="ts">
import type { PublicationUpdateCollection } from "~/types/biomedical"

defineProps<{
  actionLabel?: string
  actionTo?: string
  collection: PublicationUpdateCollection
  emptyTitle: string
  headingId: string
  kicker?: string
  title: string
}>()
</script>

<template>
  <section
    class="publication-update-list"
    :aria-labelledby="headingId"
  >
    <div class="section-heading">
      <div>
        <p v-if="kicker" class="section-kicker">
          {{ kicker }}
        </p>
        <h2 :id="headingId">
          {{ title }}
        </h2>
      </div>
      <NuxtLink
        v-if="actionTo && actionLabel"
        class="button button--secondary"
        :to="actionTo"
      >
        {{ actionLabel }}
      </NuxtLink>
    </div>

    <p
      v-if="collection.items.length === 0"
      class="publication-update-list__empty"
      data-publication-empty
    >
      {{ emptyTitle }}
    </p>
    <div v-else class="publication-update-list__items">
      <PublicationEventCard
        v-for="item in collection.items"
        :key="`${item.paper.id}:${item.event.kind}:${item.event.date}`"
        :item="item"
      />
    </div>
  </section>
</template>

<style scoped>
.publication-update-list {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
  padding: var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  box-shadow: var(--shadow-1);
}

.publication-update-list__items {
  display: grid;
}

.publication-update-list__empty {
  margin: 0;
  padding: var(--space-6) 0;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}
</style>
