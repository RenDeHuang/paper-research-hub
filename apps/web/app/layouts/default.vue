<script setup lang="ts">
const route = useRoute()
const main = useTemplateRef<HTMLElement>("main")

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
    <AppHeader />
    <div class="app-shell__content shell-container">
      <DiscoveryRail />
      <main id="main-content" ref="main" class="app-shell__main" tabindex="-1">
        <slot />
      </main>
    </div>
    <AppFooter />
  </div>
</template>
