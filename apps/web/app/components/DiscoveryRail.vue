<script setup lang="ts">
export interface DiscoveryRailItem {
  description?: string
  label: string
  to: string
}

const props = withDefaults(
  defineProps<{
    popularMethods?: DiscoveryRailItem[]
    popularTopics?: DiscoveryRailItem[]
    quickFilters?: DiscoveryRailItem[]
    savedViews?: DiscoveryRailItem[]
  }>(),
  {
    popularMethods: () => [],
    popularTopics: () => [],
    quickFilters: () => [],
    savedViews: () => [],
  },
)

const groups = computed(() => [
  {
    emptyMessage: "暂无快捷筛选",
    id: "quick-filters",
    items: props.quickFilters,
    slotName: "quick-filters",
    title: "快捷筛选",
  },
  {
    emptyMessage: "等待首次同步",
    id: "popular-topics",
    items: props.popularTopics,
    slotName: "popular-topics",
    title: "热门 Topic",
  },
  {
    emptyMessage: "等待首次同步",
    id: "popular-methods",
    items: props.popularMethods,
    slotName: "popular-methods",
    title: "热门 Method",
  },
  {
    emptyMessage: "暂无保存视图",
    id: "saved-views",
    items: props.savedViews,
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
    >
      <h2>{{ group.title }}</h2>
      <slot :name="group.slotName" :items="group.items">
        <ul>
          <li v-for="item in group.items" :key="`${group.id}:${item.to}`">
            <NuxtLink :to="item.to">
              <span>{{ item.label }}</span>
              <small v-if="item.description">{{ item.description }}</small>
            </NuxtLink>
          </li>
        </ul>
        <p
          v-if="group.items.length === 0"
          class="discovery-rail__empty"
          :data-discovery-empty="group.id"
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
