<template>
  <footer class="app-footer">
    <div class="app-footer__inner shell-container">
      <p class="app-footer__brand">
        medpaperhub
      </p>
      <p>
        医学与生物学 · 全部 JCR Q1
      </p>
    </div>
  </footer>
</template>

<style scoped>
.app-footer {
  border-top: 1px solid var(--color-border);
  background: var(--color-surface-subtle);
}

.app-footer__inner {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-5);
  align-items: center;
  justify-content: space-between;
  padding-top: var(--space-6);
  padding-bottom: var(--space-6);
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}

.app-footer p {
  margin: 0;
}

.app-footer__brand {
  color: var(--color-text-strong);
  font-weight: 800;
}
</style>
