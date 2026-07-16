<script setup lang="ts">
useHead({
  title: "Paper Research Hub",
  meta: [
    {
      name: "description",
      content: "以来源、覆盖和证据为边界的论文研究情报门户。",
    },
  ],
})
</script>

<template>
  <div class="home-page shell-container">
    <section class="home-page__intro" aria-labelledby="home-heading">
      <p class="eyebrow">
        Paper Research Hub
      </p>
      <h1 id="home-heading">
        论文研究情报，从发现到证据
      </h1>
      <p class="home-page__description">
        搜索论文与研究实体，并在后续数据接入后核查趋势、机会、来源与缺失信号。
      </p>
      <SearchCommand />
    </section>

    <section class="home-page__data" aria-labelledby="data-heading">
      <p class="section-kicker">
        数据接入状态
      </p>
      <DataState
        state="empty"
        title="等待 API 数据"
        message="当前页面只验证稳定视觉 shell；后续 API agent 将接入真实论文与分析结果，不展示模拟或补位数据。"
      />
    </section>
  </div>
</template>

<style scoped>
.home-page {
  display: grid;
  gap: var(--space-10);
  padding-top: var(--space-10);
  padding-bottom: var(--space-16);
}

.home-page__intro {
  display: grid;
  gap: var(--space-4);
  max-width: 800px;
}

.eyebrow,
.section-kicker {
  margin: 0;
  color: var(--color-info);
  font-size: var(--text-xs-size);
  font-weight: 800;
  letter-spacing: 0.08em;
  line-height: var(--text-xs-line);
}

h1 {
  max-width: 18ch;
  margin: 0;
  color: var(--color-text-strong);
  font-size: clamp(var(--text-2xl-size), 7vw, var(--text-3xl-size));
  line-height: var(--text-3xl-line);
  overflow-wrap: anywhere;
}

.home-page__description {
  max-width: 68ch;
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-lg-size);
  line-height: 1.65;
}

.home-page__data {
  display: grid;
  gap: var(--space-3);
}

@media (min-width: 768px) {
  .home-page {
    gap: var(--space-12);
    padding-top: var(--space-16);
  }

  h1 {
    font-size: var(--text-3xl-size);
  }
}

@media (min-width: 1440px) {
  h1 {
    font-size: var(--text-4xl-size);
    line-height: var(--text-4xl-line);
  }
}
</style>
