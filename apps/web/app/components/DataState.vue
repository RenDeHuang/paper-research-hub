<script setup lang="ts">
type DataStateKind =
  | "empty"
  | "error"
  | "insufficient"
  | "loading"
  | "missing"
  | "restricted"
  | "stale"
  | "unknown"

const props = defineProps<{
  actionLabel?: string
  actionTo?: string
  message?: string
  state: DataStateKind
  title: string
}>()

const emit = defineEmits<{
  action: []
}>()

const headingId = `data-state-${useId()}`
const stateLabels: Record<DataStateKind, string> = {
  empty: "尚无数据",
  error: "加载失败",
  insufficient: "证据不足",
  loading: "加载中",
  missing: "数据缺失",
  restricted: "访问受限",
  stale: "数据可能过时",
  unknown: "覆盖未知",
}
const role = computed(() => {
  if (props.state === "error") {
    return "alert"
  }
  if (props.state === "loading") {
    return "status"
  }
  return "region"
})
</script>

<template>
  <section
    class="data-state"
    :data-state="state"
    :role="role"
    :aria-labelledby="headingId"
    :aria-live="state === 'loading' ? 'polite' : undefined"
  >
    <span class="data-state__mark" aria-hidden="true" />
    <div class="data-state__content">
      <span class="data-state__label">
        {{ stateLabels[state] }}
      </span>
      <h2 :id="headingId">
        {{ title }}
      </h2>
      <p v-if="message">
        {{ message }}
      </p>
      <NuxtLink
        v-if="actionLabel && actionTo"
        class="button button--secondary"
        :to="actionTo"
      >
        {{ actionLabel }}
      </NuxtLink>
      <button
        v-else-if="actionLabel"
        class="button button--secondary"
        type="button"
        @click="emit('action')"
      >
        {{ actionLabel }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.data-state {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: var(--space-4);
  align-items: start;
  padding: var(--space-5);
  border: 1px solid var(--color-border);
  border-left: 4px solid var(--color-info);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.data-state[data-state="error"] {
  border-left-color: var(--color-negative);
  background: var(--color-negative-subtle);
}

.data-state[data-state="insufficient"],
.data-state[data-state="missing"],
.data-state[data-state="restricted"],
.data-state[data-state="stale"] {
  border-left-color: var(--color-caution);
}

.data-state__mark {
  width: 14px;
  height: 14px;
  margin-top: 5px;
  border: 2px solid currentColor;
  border-radius: 50%;
  color: var(--color-info);
}

[data-state="error"] .data-state__mark {
  border-radius: var(--radius-sm);
  color: var(--color-negative);
}

[data-state="insufficient"] .data-state__mark,
[data-state="missing"] .data-state__mark,
[data-state="restricted"] .data-state__mark,
[data-state="stale"] .data-state__mark {
  border-style: dashed;
  color: var(--color-caution);
}

[data-state="missing"] .data-state__mark {
  border-radius: var(--radius-sm);
}

[data-state="unknown"] .data-state__mark {
  border-style: dashed;
}

.data-state__content {
  min-width: 0;
}

.data-state__label {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  margin-bottom: var(--space-1);
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm);
  background: var(--color-neutral-bg);
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  font-weight: 750;
  line-height: var(--text-xs-line);
}

h2 {
  margin: 0;
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
  line-height: var(--text-lg-line);
}

p {
  max-width: 68ch;
  margin: var(--space-2) 0 0;
  color: var(--color-text-muted);
}

.button {
  margin-top: var(--space-4);
}
</style>
