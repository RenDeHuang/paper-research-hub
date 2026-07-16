<script setup lang="ts">
import { resolveRouteLabel } from "~/utils/routeLabel"

const route = useRoute()
const main = useTemplateRef<HTMLElement>("main")
const routeLabel = computed(() => resolveRouteLabel(route))
const quickFilters = {
  items: [
    { label: "有代码", to: "/papers?has_code=true" },
    { label: "有数据", to: "/papers?has_data=true" },
    { label: "有 Benchmark", to: "/papers?has_benchmark=true" },
    { label: "趋势优先", to: "/papers?sort=trend_desc" },
  ],
  state: "known" as const,
}
const popularTopics = {
  label: "请在 Topic 页加载真实目录",
  state: "unknown" as const,
}
const popularMethods = {
  label: "请在 Method 页加载真实目录",
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
        :quick-filters="quickFilters"
        :popular-topics="popularTopics"
        :popular-methods="popularMethods"
        :saved-views="savedViews"
      />
      <main id="main-content" ref="main" class="app-shell__main" tabindex="-1">
        <slot />
      </main>
    </div>
    <AppFooter />
  </div>
</template>
