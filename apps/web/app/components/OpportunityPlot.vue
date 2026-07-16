<script setup lang="ts">
type OpportunityStatus =
  | "insufficient-evidence"
  | "not-recommended-now"
  | "proceed-with-caution"
  | "worth-pursuing"

interface OpportunityPlotPoint {
  id: string
  label: string
  missingSignals: string[]
  status: OpportunityStatus
  x: number
  y: number
}

const props = defineProps<{
  points: OpportunityPlotPoint[]
  summary: string
  xAxisLabel: string
  yAxisLabel: string
}>()

const statusLabels: Record<OpportunityStatus, string> = {
  "worth-pursuing": "值得做",
  "proceed-with-caution": "谨慎做",
  "not-recommended-now": "当前不建议做",
  "insufficient-evidence": "证据不足",
}

const chart = {
  bottom: 270,
  left: 48,
  right: 500,
  top: 20,
} as const

function coordinate(point: OpportunityPlotPoint) {
  if (point.x < 0 || point.x > 100 || point.y < 0 || point.y > 100) {
    throw new Error("OpportunityPlot coordinates must be between 0 and 100.")
  }

  return {
    x: chart.left + (point.x / 100) * (chart.right - chart.left),
    y: chart.bottom - (point.y / 100) * (chart.bottom - chart.top),
  }
}

function diamondPoints(point: OpportunityPlotPoint) {
  const { x, y } = coordinate(point)
  return `${x},${y - 7} ${x + 7},${y} ${x},${y + 7} ${x - 7},${y}`
}

function pointLabel(point: OpportunityPlotPoint) {
  const missingSignals =
    point.missingSignals.length > 0 ? point.missingSignals.join("；") : "无"
  return `${point.label}，${statusLabels[point.status]}，${props.xAxisLabel} ${point.x}，${props.yAxisLabel} ${point.y}，缺失信号 ${missingSignals}`
}
</script>

<template>
  <svg
    class="opportunity-plot"
    viewBox="0 0 520 320"
    role="img"
    :aria-label="summary"
  >
    <line
      :x1="chart.left"
      :y1="chart.bottom"
      :x2="chart.right"
      :y2="chart.bottom"
      class="opportunity-plot__axis"
    />
    <line
      :x1="chart.left"
      :y1="chart.top"
      :x2="chart.left"
      :y2="chart.bottom"
      class="opportunity-plot__axis"
    />
    <line
      :x1="(chart.left + chart.right) / 2"
      :y1="chart.top"
      :x2="(chart.left + chart.right) / 2"
      :y2="chart.bottom"
      class="opportunity-plot__gridline"
    />
    <line
      :x1="chart.left"
      :y1="(chart.top + chart.bottom) / 2"
      :x2="chart.right"
      :y2="(chart.top + chart.bottom) / 2"
      class="opportunity-plot__gridline"
    />

    <text x="274" y="310" class="opportunity-plot__axis-label">
      {{ xAxisLabel }}
    </text>
    <text
      x="16"
      y="145"
      class="opportunity-plot__axis-label"
      transform="rotate(-90 16 145)"
    >
      {{ yAxisLabel }}
    </text>

    <g
      v-for="point in points"
      :key="point.id"
      role="graphics-symbol"
      :aria-label="pointLabel(point)"
    >
      <circle
        v-if="point.status === 'worth-pursuing'"
        :cx="coordinate(point).x"
        :cy="coordinate(point).y"
        r="7"
        class="opportunity-plot__point opportunity-plot__point--worth"
        data-opportunity-shape="circle"
        :data-status="point.status"
      />
      <polygon
        v-else-if="point.status === 'proceed-with-caution'"
        :points="diamondPoints(point)"
        class="opportunity-plot__point opportunity-plot__point--caution"
        data-opportunity-shape="diamond"
        :data-status="point.status"
      />
      <rect
        v-else-if="point.status === 'not-recommended-now'"
        :x="coordinate(point).x - 7"
        :y="coordinate(point).y - 7"
        width="14"
        height="14"
        class="opportunity-plot__point opportunity-plot__point--negative"
        data-opportunity-shape="square"
        :data-status="point.status"
      />
      <circle
        v-else
        :cx="coordinate(point).x"
        :cy="coordinate(point).y"
        r="7"
        class="opportunity-plot__point opportunity-plot__point--insufficient"
        data-opportunity-shape="hollow-circle"
        :data-status="point.status"
      />
    </g>
  </svg>
</template>

<style scoped>
.opportunity-plot {
  width: 100%;
  min-height: 260px;
  overflow: visible;
}

.opportunity-plot__axis {
  stroke: var(--color-border-control);
  stroke-width: 1.5;
}

.opportunity-plot__gridline {
  stroke: var(--color-border);
  stroke-dasharray: 4 5;
  stroke-width: 1;
}

.opportunity-plot__axis-label {
  fill: var(--color-text-muted);
  font-family: var(--font-ui);
  font-size: 12px;
  text-anchor: middle;
}

.opportunity-plot__point {
  stroke-width: 2;
}

.opportunity-plot__point--worth {
  fill: var(--color-positive);
  stroke: var(--color-positive);
}

.opportunity-plot__point--caution {
  fill: var(--color-caution-bg);
  stroke: var(--color-caution);
}

.opportunity-plot__point--negative {
  fill: var(--color-negative);
  stroke: var(--color-negative);
}

.opportunity-plot__point--insufficient {
  fill: var(--color-surface);
  stroke: var(--color-text-muted);
  stroke-dasharray: 3 2;
}
</style>
