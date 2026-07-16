<script setup lang="ts">
import { resolveRouteLabel } from "~/utils/routeLabel"

const route = useRoute()
const main = useTemplateRef<HTMLElement>("main")
const routeLabel = computed(() => resolveRouteLabel(route))
const quickFilters = [
  { label: "JIF ≥ 10", to: "/papers?jif_min=10" },
  { label: "JCR Q1", to: "/papers?jcr_quartile=Q1" },
]

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
        :popular-topics="[]"
        :popular-methods="[]"
        :saved-views="[]"
      />
      <main id="main-content" ref="main" class="app-shell__main" tabindex="-1">
        <slot />
      </main>
    </div>
    <AppFooter />
  </div>
</template>
