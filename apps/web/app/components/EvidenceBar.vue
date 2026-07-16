<script setup lang="ts">
type EvidenceValue =
  | { state: "known"; value: number }
  | { label?: string; state: "missing" }
  | { label?: string; state: "unknown" }

const props = withDefaults(
  defineProps<{
    description?: string
    label: string
    max?: number
    min?: number
    source?: string
    unit?: string
    value: EvidenceValue
  }>(),
  {
    description: undefined,
    max: 100,
    min: 0,
    source: undefined,
    unit: "%",
  },
)

const valueText = computed(() => {
  if (props.value.state === "missing") {
    return props.value.label ?? "缺失"
  }
  if (props.value.state === "unknown") {
    return props.value.label ?? "未覆盖"
  }
  return `${props.value.value}${props.unit}`
})

const fillPercentage = computed(() => {
  if (props.value.state !== "known") {
    return undefined
  }
  if (props.max <= props.min) {
    throw new Error("EvidenceBar max must be greater than min.")
  }
  if (props.value.value < props.min || props.value.value > props.max) {
    throw new Error("EvidenceBar value must be within min and max.")
  }
  return ((props.value.value - props.min) / (props.max - props.min)) * 100
})

const accessibleLabel = computed(() => {
  const parts = [`${props.label}：${valueText.value}`]
  if (props.description) {
    parts.push(props.description)
  }
  if (props.source) {
    parts.push(`来源：${props.source}`)
  }
  return parts.join("；")
})
</script>

<template>
  <div
    class="evidence-bar"
    :data-value-state="value.state"
    role="img"
    :aria-label="accessibleLabel"
  >
    <div class="evidence-bar__heading">
      <span class="evidence-bar__label">{{ label }}</span>
      <span class="evidence-bar__value">{{ valueText }}</span>
    </div>

    <div
      v-if="value.state === 'known'"
      class="evidence-bar__track"
      aria-hidden="true"
    >
      <span
        class="evidence-bar__fill"
        :style="{ width: `${fillPercentage}%` }"
      />
      <span
        v-if="value.value === min"
        class="evidence-bar__zero-marker"
        data-zero-marker
      />
    </div>
    <div
      v-else
      class="evidence-bar__track evidence-bar__missing"
      aria-hidden="true"
    />

    <p v-if="description" class="evidence-bar__description">
      {{ description }}
    </p>
    <p v-if="source" class="evidence-bar__source">
      来源：{{ source }}
    </p>
  </div>
</template>

<style scoped>
.evidence-bar {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
}

.evidence-bar__heading {
  display: flex;
  gap: var(--space-3);
  align-items: baseline;
  justify-content: space-between;
}

.evidence-bar__label {
  color: var(--color-text);
  font-weight: 700;
}

.evidence-bar__value {
  color: var(--color-text-strong);
  font-family: var(--font-data);
  font-size: var(--text-sm-size);
  font-variant-numeric: tabular-nums;
  font-weight: 700;
  line-height: var(--text-sm-line);
}

[data-value-state="missing"] .evidence-bar__value,
[data-value-state="unknown"] .evidence-bar__value {
  color: var(--color-text-muted);
  font-family: var(--font-ui);
  font-style: italic;
}

.evidence-bar__track {
  position: relative;
  height: 12px;
  overflow: hidden;
  border-radius: var(--radius-sm);
  background: var(--color-track);
}

.evidence-bar__fill {
  display: block;
  height: 100%;
  background: var(--color-positive);
}

.evidence-bar__zero-marker {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  width: 3px;
  background: var(--color-positive);
}

.evidence-bar__missing {
  border: 1px dashed var(--color-border-control);
  background: transparent;
}

.evidence-bar__description,
.evidence-bar__source {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  line-height: var(--text-xs-line);
}
</style>
