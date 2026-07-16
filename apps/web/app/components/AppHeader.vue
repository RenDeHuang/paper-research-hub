<script setup lang="ts">
const route = useRoute()
const menuOpen = ref(false)
const menuButton = useTemplateRef<HTMLButtonElement>("menuButton")

const navigation = [
  { label: "首页", to: "/" },
  { label: "论文", to: "/papers" },
  { label: "趋势", to: "/trends" },
  { label: "研究机会", to: "/opportunities" },
] as const

function isCurrent(to: string) {
  if (to === "/") {
    return route.path === to
  }
  return route.path === to || route.path.startsWith(`${to}/`)
}

function toggleMenu() {
  menuOpen.value = !menuOpen.value
}

function closeMenu({ restoreFocus = false } = {}) {
  if (!menuOpen.value) {
    return
  }
  menuOpen.value = false
  if (restoreFocus) {
    nextTick(() => menuButton.value?.focus())
  }
}

watch(
  () => route.fullPath,
  () => closeMenu(),
)

watch(menuOpen, (open) => {
  document.documentElement.classList.toggle("menu-open", open)
})

onBeforeUnmount(() => {
  document.documentElement.classList.remove("menu-open")
})
</script>

<template>
  <header class="app-header" @keydown.esc="closeMenu({ restoreFocus: true })">
    <div class="app-header__inner shell-container">
      <NuxtLink class="app-header__brand" to="/" aria-label="Paper Research Hub 首页">
        Paper Research Hub
      </NuxtLink>

      <nav
        id="primary-navigation"
        class="app-header__navigation"
        :class="{ 'is-open': menuOpen }"
        aria-label="一级导航"
      >
        <ul>
          <li v-for="item in navigation" :key="item.to">
            <a
              :href="item.to"
              :aria-current="isCurrent(item.to) ? 'page' : undefined"
              @click="closeMenu()"
            >
              {{ item.label }}
            </a>
          </li>
        </ul>
      </nav>

      <a
        class="app-header__search"
        href="/#global-search"
        aria-label="搜索"
        @click="closeMenu()"
      >
        搜索
      </a>

      <button
        ref="menuButton"
        class="app-header__menu-button"
        type="button"
        aria-controls="primary-navigation"
        :aria-expanded="menuOpen"
        :aria-label="menuOpen ? '关闭一级导航' : '打开一级导航'"
        @click="toggleMenu"
      >
        菜单
      </button>
    </div>
  </header>
</template>

<style scoped>
.app-header {
  position: sticky;
  z-index: var(--z-sticky);
  top: 0;
  min-height: 64px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-surface);
  box-shadow: var(--shadow-1);
}

.app-header__inner {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  gap: var(--space-2);
  align-items: center;
  min-height: 64px;
}

.app-header__brand {
  min-width: 0;
  overflow: hidden;
  color: var(--color-primary);
  font-size: var(--text-sm-size);
  font-weight: 800;
  line-height: 1.2;
  text-decoration: none;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-header__search,
.app-header__menu-button {
  display: inline-flex;
  min-width: 52px;
  min-height: 44px;
  align-items: center;
  justify-content: center;
  padding: 0 var(--space-3);
  border-radius: var(--radius-md);
  font-size: var(--text-sm-size);
  font-weight: 700;
  text-decoration: none;
  touch-action: manipulation;
}

.app-header__search {
  color: var(--color-link);
}

.app-header__menu-button {
  border: 1px solid var(--color-border-control);
  background: var(--color-surface);
  color: var(--color-primary);
  cursor: pointer;
}

.app-header__search:hover,
.app-header__menu-button:hover {
  background: var(--color-surface-subtle);
}

.app-header__search:active,
.app-header__menu-button:active {
  background: var(--warm-100);
}

.app-header__navigation {
  position: absolute;
  top: calc(100% + 1px);
  right: var(--shell-gutter);
  left: var(--shell-gutter);
  display: none;
  padding: var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: 0 0 var(--radius-md) var(--radius-md);
  background: var(--color-surface);
  box-shadow: var(--shadow-2);
}

.app-header__navigation.is-open {
  display: block;
}

.app-header__navigation ul {
  display: grid;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.app-header__navigation a {
  position: relative;
  display: flex;
  min-height: 44px;
  align-items: center;
  padding: 0 var(--space-3);
  border-radius: var(--radius-md);
  color: var(--color-text-strong);
  font-size: var(--text-sm-size);
  font-weight: 650;
  text-decoration: none;
  touch-action: manipulation;
}

.app-header__navigation a:hover {
  background: var(--color-surface-subtle);
}

.app-header__navigation a:active {
  background: var(--warm-100);
}

.app-header__navigation a[aria-current="page"] {
  color: var(--color-primary);
  font-weight: 800;
}

.app-header__navigation a[aria-current="page"]::after {
  position: absolute;
  right: var(--space-3);
  bottom: 6px;
  left: var(--space-3);
  height: 3px;
  border-radius: var(--radius-sm);
  background: var(--color-primary);
  content: "";
}

@media (min-width: 768px) {
  .app-header {
    min-height: 72px;
  }

  .app-header__inner {
    grid-template-columns: auto minmax(0, 1fr) auto;
    min-height: 72px;
  }

  .app-header__brand {
    font-size: var(--text-base-size);
  }

  .app-header__navigation {
    position: static;
    display: block;
    padding: 0;
    border: 0;
    background: transparent;
    box-shadow: none;
  }

  .app-header__navigation ul {
    display: flex;
    gap: var(--space-2);
    justify-content: center;
  }

  .app-header__navigation a[aria-current="page"]::after {
    right: var(--space-3);
    bottom: 0;
    left: var(--space-3);
  }

  .app-header__menu-button {
    display: none;
  }
}
</style>
