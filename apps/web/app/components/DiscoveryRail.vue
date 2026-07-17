<script setup lang="ts">
export interface DiscoveryRailItem {
  description?: string
  label: string
  to: string
}

export type DiscoveryRailCollection =
  | {
      items: DiscoveryRailItem[]
      state: "known"
    }
  | {
      label: string
      state: "missing" | "unknown"
    }

const props = defineProps<{
  activeJournals: DiscoveryRailCollection
  quickFilters: DiscoveryRailCollection
  savedViews: DiscoveryRailCollection
  trendingSubjects: DiscoveryRailCollection
}>()

function resolveCollection(
  collection: DiscoveryRailCollection,
  emptyMessage: string,
) {
  if (collection.state === "known") {
    return {
      emptyMessage,
      items: collection.items,
      state: collection.items.length > 0 ? "known" : "empty",
    }
  }

  return {
    emptyMessage: collection.label,
    items: [],
    state: collection.state,
  }
}

const groups = computed(() => [
  {
    id: "quick-filters",
    ...resolveCollection(props.quickFilters, "暂无快捷筛选"),
    slotName: "quick-filters",
    title: "快捷筛选",
  },
  {
    id: "trending-subjects",
    ...resolveCollection(props.trendingSubjects, "当前没有学科趋势"),
    slotName: "trending-subjects",
    title: "学科趋势",
  },
  {
    id: "active-journals",
    ...resolveCollection(props.activeJournals, "当前没有活跃期刊"),
    slotName: "active-journals",
    title: "活跃期刊",
  },
  {
    id: "saved-views",
    ...resolveCollection(props.savedViews, "暂无保存视图"),
    slotName: "saved-views",
    title: "保存视图",
  },
])
</script>

<template>
  <aside class="discovery-rail" aria-label="发现导航">
    <section
      v-for="group in groups"
      :key="group.id"
      class="discovery-rail__section"
      :data-discovery-group="group.id"
      :data-discovery-state="group.state"
    >
      <h2>{{ group.title }}</h2>
      <slot
        :name="group.slotName"
        :items="group.items"
        :state="group.state"
      >
        <ul v-if="group.items.length > 0">
          <li v-for="item in group.items" :key="`${group.id}:${item.to}`">
            <NuxtLink :to="item.to">
              <span>{{ item.label }}</span>
              <small v-if="item.description">{{ item.description }}</small>
            </NuxtLink>
          </li>
        </ul>
        <p
          v-else
          class="discovery-rail__empty"
          :data-discovery-empty="group.id"
          :data-discovery-state="group.state"
        >
          {{ group.emptyMessage }}
        </p>
      </slot>
    </section>
  </aside>
</template>

<style scoped>
.discovery-rail {
  display: grid;
  gap: var(--space-6);
  align-content: start;
  min-width: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.discovery-rail__section {
  display: grid;
  gap: var(--space-2);
}

h2 {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  font-weight: 800;
  letter-spacing: 0.06em;
  line-height: var(--text-xs-line);
}

ul {
  display: grid;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.discovery-rail__empty {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

a {
  display: grid;
  min-height: 44px;
  align-content: center;
  gap: var(--space-1);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  color: var(--color-text-strong);
  font-size: var(--text-sm-size);
  font-weight: 700;
  line-height: var(--text-sm-line);
  text-decoration: none;
}

a:hover {
  background: var(--color-surface-subtle);
}

a:active {
  background: var(--color-surface-pressed);
}

small {
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  font-weight: 400;
  line-height: var(--text-xs-line);
}
</style>
