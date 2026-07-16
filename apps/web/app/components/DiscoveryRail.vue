<script setup lang="ts">
export interface DiscoveryRailItem {
  description?: string
  label: string
  to: string
}

export interface DiscoveryRailSection {
  id: string
  items: DiscoveryRailItem[]
  title: string
}

const props = defineProps<{
  sections?: DiscoveryRailSection[]
}>()

const defaultSections: DiscoveryRailSection[] = [
  {
    id: "navigation",
    items: [
      { label: "浏览论文", to: "/papers" },
      { label: "查看趋势", to: "/trends" },
      { label: "研究机会", to: "/opportunities" },
    ],
    title: "发现入口",
  },
  {
    id: "policy",
    items: [
      {
        description: "查看精选范围、证据和限制",
        label: "精选策略",
        to: "/papers?view=curated",
      },
    ],
    title: "策略入口",
  },
]

const resolvedSections = computed(() => props.sections ?? defaultSections)
</script>

<template>
  <aside class="discovery-rail" aria-label="发现导航">
    <slot :sections="resolvedSections">
      <section
        v-for="section in resolvedSections"
        :key="section.id"
        class="discovery-rail__section"
      >
        <h2>{{ section.title }}</h2>
        <ul>
          <li v-for="item in section.items" :key="`${section.id}:${item.to}`">
            <NuxtLink :to="item.to">
              <span>{{ item.label }}</span>
              <small v-if="item.description">{{ item.description }}</small>
            </NuxtLink>
          </li>
        </ul>
      </section>
    </slot>
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
