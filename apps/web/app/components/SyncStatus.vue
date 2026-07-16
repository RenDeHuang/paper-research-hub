<script setup lang="ts">
import {
  presentDataValue,
  type DataValue,
} from "~/utils/dataValue"

const props = defineProps<{
  coverage: DataValue<number | string>
  dataRange: DataValue<string>
  updatedAt: DataValue<string>
}>()

const items = computed(() => [
  { label: "更新时间", value: presentDataValue(props.updatedAt) },
  { label: "数据范围", value: presentDataValue(props.dataRange) },
  { label: "覆盖率", value: presentDataValue(props.coverage) },
])
</script>

<template>
  <section id="sync-status" class="sync-status" aria-label="数据状态">
    <dl>
      <div v-for="item in items" :key="item.label">
        <dt>{{ item.label }}</dt>
        <dd
          :data-value-state="item.value.state"
          :data-value-kind="item.value.kind"
        >
          {{ item.value.text }}
        </dd>
      </div>
    </dl>
  </section>
</template>

<style scoped>
.sync-status {
  min-width: 0;
}

dl {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(144px, 1fr));
  gap: var(--space-2);
  margin: 0;
}

dl > div {
  display: grid;
  gap: var(--space-1);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

dt {
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  line-height: var(--text-xs-line);
}

dd {
  margin: 0;
  color: var(--color-text-strong);
  font-size: var(--text-sm-size);
  font-variant-numeric: tabular-nums;
  font-weight: 700;
  line-height: var(--text-sm-line);
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-family: var(--font-ui);
  font-style: italic;
  font-weight: 600;
}
</style>
