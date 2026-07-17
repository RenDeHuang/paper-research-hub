<script setup lang="ts">
import { resolveRouteLabel } from "~/utils/routeLabel"

const route = useRoute()
const main = useTemplateRef<HTMLElement>("main")
const routeLabel = computed(() => resolveRouteLabel(route))
const quickFilters = {
  items: [
    { label: "最新精选", to: "/papers?sort=published_at_desc" },
    { label: "引用优先", to: "/papers?sort=citations_desc" },
    { label: "趋势优先", to: "/papers?sort=trend_desc" },
    { label: "研究机会", to: "/opportunities" },
  ],
  state: "known" as const,
}
const trendingSubjects = {
  label: "等待学科趋势生成",
  state: "unknown" as const,
}
const activeJournals = {
  label: "等待期刊活跃度生成",
  state: "unknown" as const,
}
const savedViews = {
  items: [],
  state: "known" as const,
}

watch(
  () => route.path,
  async () => {
    await nextTick()
    main.value?.focus({ preventScroll: true })
  },
)
</script>

<template>
  <div class="app-shell">
    <a class="skip-link" href="#main-content">
      跳到主要内容
    </a>
    <AppHeader :route-label="routeLabel" />
    <div class="app-shell__content shell-container">
      <DiscoveryRail
        :active-journals="activeJournals"
        :quick-filters="quickFilters"
        :saved-views="savedViews"
        :trending-subjects="trendingSubjects"
      />
      <main id="main-content" ref="main" class="app-shell__main" tabindex="-1">
        <slot />
      </main>
    </div>
    <AppFooter />
  </div>
</template>
