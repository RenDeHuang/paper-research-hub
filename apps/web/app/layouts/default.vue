<script setup lang="ts">
import { resolveRouteLabel } from "~/utils/routeLabel"

const route = useRoute()
const main = useTemplateRef<HTMLElement>("main")
const routeLabel = computed(() => resolveRouteLabel(route))

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
      <main id="main-content" ref="main" class="app-shell__main" tabindex="-1">
        <slot />
      </main>
    </div>
    <AppFooter />
  </div>
</template>
