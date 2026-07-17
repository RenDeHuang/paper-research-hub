<template>
  <footer class="app-footer">
    <div class="app-footer__inner shell-container">
      <p class="app-footer__brand">
        medpaperhub
      </p>
      <p>
        医学与生物学论文、期刊趋势和研究机会均以可追溯来源、明确覆盖和独立缺失状态呈现。
      </p>
      <a href="/#global-search">
        返回搜索
      </a>
    </div>
  </footer>
</template>

<style scoped>
.app-footer {
  border-top: 1px solid var(--color-border);
  background: var(--color-surface-subtle);
}

.app-footer__inner {
  display: grid;
  gap: var(--space-3);
  padding-top: var(--space-8);
  padding-bottom: var(--space-8);
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.app-footer p {
  max-width: 68ch;
  margin: 0;
}

.app-footer__brand {
  color: var(--color-text-strong);
  font-weight: 800;
}

.app-footer a {
  width: fit-content;
  min-height: 44px;
  align-content: center;
  color: var(--color-link);
}

@media (min-width: 768px) {
  .app-footer__inner {
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: start;
  }
}
</style>
