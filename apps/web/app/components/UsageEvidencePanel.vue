<script setup lang="ts">
import type { PaperDetail } from "~/types/catalog"

const props = defineProps<{
  paper: PaperDetail
}>()

const articleUsage = computed(() => props.paper.article_usage)
</script>

<template>
  <section
    class="detail-section usage-evidence"
    aria-labelledby="usage-evidence-heading"
  >
    <p class="section-kicker">
      使用指标
    </p>
    <h2 id="usage-evidence-heading">
      下载与使用证据
    </h2>
    <dl class="detail-list">
      <div
        data-article-usage
        :data-value-state="articleUsage.state"
      >
        <dt>下载/使用量</dt>
        <dd>缺失 · 暂无权威来源</dd>
      </div>
    </dl>
    <p class="usage-evidence__note">
      当前未接入可审计、可比较的 article-level usage metric，因此不发布数值。
    </p>
  </section>
</template>

<style scoped>
.usage-evidence {
  gap: var(--space-4);
}

.usage-evidence__note {
  max-width: 72ch;
  margin: 0;
  color: var(--color-text-muted);
  line-height: 1.65;
}

[data-value-state="missing"] dd {
  color: var(--color-text-muted);
  font-style: italic;
}
</style>
