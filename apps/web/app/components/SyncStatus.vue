<script setup lang="ts">
const props = defineProps<{
  calendarDate: string
  calendarTimezone: "UTC"
  generatedAt: string
  jcrMetricYear: number
}>()

const updatedAt = computed(() => {
  const value = new Date(props.generatedAt)
  if (!Number.isFinite(value.valueOf())) {
    throw new Error(`API 返回了无效时间: ${props.generatedAt}`)
  }
  const date = new Intl.DateTimeFormat("zh-CN", {
    day: "2-digit",
    month: "2-digit",
    timeZone: "UTC",
    year: "numeric",
  }).format(value).replaceAll("/", "-")
  const time = new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    hour12: false,
    minute: "2-digit",
    timeZone: "UTC",
  }).format(value)
  return `${date} ${time} UTC`
})

const items = computed(() => [
  { label: "日报日期", value: props.calendarDate },
  { label: "更新", value: updatedAt.value },
  { label: "时区", value: props.calendarTimezone },
  {
    label: "收录范围",
    value: `JCR ${props.jcrMetricYear} · Q1 / JIF ≥ 10`,
  },
])
</script>

<template>
  <section id="sync-status" class="sync-status" aria-label="日报状态">
    <dl>
      <div v-for="item in items" :key="item.label">
        <dt>{{ item.label }}</dt>
        <dd>{{ item.value }}</dd>
      </div>
    </dl>
  </section>
</template>

<style scoped>
.sync-status {
  min-width: 0;
  padding: var(--space-3) var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface-subtle);
}

dl {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-5);
  align-items: center;
  margin: 0;
}

dl > div {
  display: flex;
  gap: var(--space-2);
  align-items: baseline;
}

dt {
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
}

dd {
  margin: 0;
  color: var(--color-text-strong);
  font-size: var(--text-sm-size);
  font-variant-numeric: tabular-nums;
  font-weight: 750;
}
</style>
