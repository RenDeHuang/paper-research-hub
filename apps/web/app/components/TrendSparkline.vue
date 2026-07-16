<script setup lang="ts">
type TrendPoint =
  | { label: string; state: "known"; value: number }
  | { label: string; state: "missing" }
  | { label: string; state: "unknown" }

const props = withDefaults(
  defineProps<{
    emptyMessage?: string
    emptyTitle?: string
    insufficientMessage?: string
    insufficientTitle?: string
    missingMessage?: string
    missingTitle?: string
    points: TrendPoint[]
    summary: string
    title: string
    unit?: string
  }>(),
  {
    emptyMessage: "调用方尚未提供可绘制的时间点。",
    emptyTitle: "暂无趋势数据",
    insufficientMessage: "时间点存在，但当前覆盖不足，无法绘制趋势。",
    insufficientTitle: "趋势证据不足",
    missingMessage: "时间点存在，但预期数据缺失，无法绘制趋势。",
    missingTitle: "趋势数据缺失",
    unit: "",
  },
)

const chart = {
  bottom: 86,
  left: 16,
  right: 304,
  top: 10,
} as const

const knownValues = computed(() =>
  props.points.flatMap((point) =>
    point.state === "known" ? [point.value] : [],
  ),
)

const displayState = computed<"chart" | "empty" | "insufficient" | "missing">(
  () => {
    if (props.points.length === 0) {
      return "empty"
    }
    if (knownValues.value.length > 0) {
      return "chart"
    }
    if (props.points.some((point) => point.state === "missing")) {
      return "missing"
    }
    return "insufficient"
  },
)

const stateCopy = computed(() => {
  if (displayState.value === "missing") {
    return {
      message: props.missingMessage,
      title: props.missingTitle,
    }
  }
  if (displayState.value === "insufficient") {
    return {
      message: props.insufficientMessage,
      title: props.insufficientTitle,
    }
  }
  return {
    message: props.emptyMessage,
    title: props.emptyTitle,
  }
})

const coordinates = computed(() => {
  if (knownValues.value.length === 0) {
    return []
  }

  const minimum = Math.min(...knownValues.value)
  const maximum = Math.max(...knownValues.value)
  const xSpan = chart.right - chart.left
  const ySpan = chart.bottom - chart.top
  const divisor = Math.max(props.points.length - 1, 1)

  return props.points.map((point, index) => {
    if (point.state !== "known") {
      return undefined
    }

    const x = chart.left + (index / divisor) * xSpan
    const y =
      minimum === maximum
        ? chart.top + ySpan / 2
        : chart.bottom - ((point.value - minimum) / (maximum - minimum)) * ySpan

    return { index, value: point.value, x, y }
  })
})

const lineSegments = computed(() => {
  const segments: string[] = []
  let current: string[] = []

  for (const coordinate of coordinates.value) {
    if (!coordinate) {
      if (current.length > 0) {
        segments.push(current.join(" "))
        current = []
      }
      continue
    }

    current.push(
      `${current.length === 0 ? "M" : "L"} ${coordinate.x.toFixed(2)} ${coordinate.y.toFixed(2)}`,
    )
  }

  if (current.length > 0) {
    segments.push(current.join(" "))
  }

  return segments
})

function pointText(point: TrendPoint) {
  if (point.state === "missing") {
    return "缺失"
  }
  if (point.state === "unknown") {
    return "未覆盖"
  }
  return `${point.value}${props.unit}`
}
</script>

<template>
  <figure class="trend-sparkline">
    <figcaption>
      <span class="trend-sparkline__title">{{ title }}</span>
      <span class="trend-sparkline__summary">{{ summary }}</span>
    </figcaption>

    <DataState
      v-if="displayState !== 'chart'"
      :state="displayState"
      :title="stateCopy.title"
      :message="stateCopy.message"
    />
    <svg
      v-else
      class="trend-sparkline__chart"
      viewBox="0 0 320 96"
      role="img"
      :aria-label="summary"
      preserveAspectRatio="none"
    >
      <line
        :x1="chart.left"
        y1="48"
        :x2="chart.right"
        y2="48"
        class="trend-sparkline__gridline"
        vector-effect="non-scaling-stroke"
      />
      <path
        v-for="segment in lineSegments"
        :key="segment"
        :d="segment"
        class="trend-sparkline__line"
        vector-effect="non-scaling-stroke"
      />
      <circle
        v-for="point in coordinates.filter(Boolean)"
        :key="point?.index"
        :cx="point?.x"
        :cy="point?.y"
        r="3.5"
        class="trend-sparkline__point"
        vector-effect="non-scaling-stroke"
      />
    </svg>

    <details class="data-table-disclosure">
      <summary>查看数据表</summary>
      <div class="data-table-scroll" tabindex="0">
        <table>
          <caption class="visually-hidden">
            {{ title }}完整数据
          </caption>
          <thead>
            <tr>
              <th scope="col">时间</th>
              <th scope="col">值</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="point in points" :key="point.label">
              <th scope="row">{{ point.label }}</th>
              <td :data-value-state="point.state">
                {{ pointText(point) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </details>
  </figure>
</template>

<style scoped>
.trend-sparkline {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
  margin: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

figcaption {
  display: grid;
  gap: var(--space-1);
}

.trend-sparkline__title {
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
  font-weight: 750;
  line-height: var(--text-lg-line);
}

.trend-sparkline__summary {
  max-width: 68ch;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.trend-sparkline__chart {
  display: block;
  width: 100%;
  height: 112px;
  overflow: visible;
}

.trend-sparkline__gridline {
  stroke: var(--color-border);
  stroke-width: 1;
}

.trend-sparkline__line {
  fill: none;
  stroke: var(--color-chart-primary);
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 2.5;
}

.trend-sparkline__point {
  fill: var(--color-surface);
  stroke: var(--color-chart-primary-strong);
  stroke-width: 2;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-style: italic;
}
</style>
