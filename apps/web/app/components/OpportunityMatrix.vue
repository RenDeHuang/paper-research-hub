<script setup lang="ts">
type OpportunityStatus =
  | "insufficient-evidence"
  | "not-recommended-now"
  | "proceed-with-caution"
  | "worth-pursuing"

interface OpportunityPoint {
  id: string
  label: string
  status: OpportunityStatus
  x: number
  y: number
}

const props = withDefaults(
  defineProps<{
    emptyMessage?: string
    emptyTitle?: string
    points: OpportunityPoint[]
    summary: string
    title: string
    xAxisLabel: string
    yAxisLabel: string
  }>(),
  {
    emptyMessage: "调用方尚未提供研究机会坐标。",
    emptyTitle: "暂无研究机会数据",
  },
)

const statusDefinitions: Record<
  OpportunityStatus,
  { label: string; shape: "circle" | "diamond" | "hollow-circle" | "square" }
> = {
  "worth-pursuing": {
    label: "值得做",
    shape: "circle",
  },
  "proceed-with-caution": {
    label: "谨慎做",
    shape: "diamond",
  },
  "not-recommended-now": {
    label: "当前不建议做",
    shape: "square",
  },
  "insufficient-evidence": {
    label: "证据不足",
    shape: "hollow-circle",
  },
}

const chart = {
  bottom: 270,
  left: 48,
  right: 500,
  top: 20,
} as const

function coordinate(point: OpportunityPoint) {
  if (point.x < 0 || point.x > 100 || point.y < 0 || point.y > 100) {
    throw new Error("OpportunityMatrix coordinates must be between 0 and 100.")
  }

  return {
    x: chart.left + (point.x / 100) * (chart.right - chart.left),
    y: chart.bottom - (point.y / 100) * (chart.bottom - chart.top),
  }
}

function diamondPoints(point: OpportunityPoint) {
  const { x, y } = coordinate(point)
  return `${x},${y - 7} ${x + 7},${y} ${x},${y + 7} ${x - 7},${y}`
}

function pointLabel(point: OpportunityPoint) {
  return `${point.label}，${statusDefinitions[point.status].label}，${props.xAxisLabel} ${point.x}，${props.yAxisLabel} ${point.y}`
}
</script>

<template>
  <figure class="opportunity-matrix">
    <figcaption>
      <span class="opportunity-matrix__title">{{ title }}</span>
      <span class="opportunity-matrix__summary">{{ summary }}</span>
    </figcaption>

    <ul class="opportunity-matrix__legend" aria-label="研究机会状态图例">
      <li
        v-for="(definition, status) in statusDefinitions"
        :key="status"
        :data-status="status"
      >
        <span
          class="opportunity-matrix__legend-shape"
          :data-shape="definition.shape"
          aria-hidden="true"
        />
        {{ definition.label }}
      </li>
    </ul>

    <DataState
      v-if="points.length === 0"
      state="empty"
      :title="emptyTitle"
      :message="emptyMessage"
    />
    <ul
      v-else
      class="opportunity-matrix__mobile-list"
      aria-label="研究机会列表"
    >
      <li v-for="point in points" :key="point.id">
        <strong>{{ point.label }}</strong>
        <span class="opportunity-matrix__status">
          <span
            class="opportunity-matrix__legend-shape"
            :data-shape="statusDefinitions[point.status].shape"
            aria-hidden="true"
          />
          {{ statusDefinitions[point.status].label }}
        </span>
        <span>{{ xAxisLabel }} {{ point.x }}</span>
        <span>{{ yAxisLabel }} {{ point.y }}</span>
      </li>
    </ul>
    <svg
      v-if="points.length > 0"
      class="opportunity-matrix__chart"
      viewBox="0 0 520 320"
      role="img"
      :aria-label="summary"
    >
      <line
        :x1="chart.left"
        :y1="chart.bottom"
        :x2="chart.right"
        :y2="chart.bottom"
        class="opportunity-matrix__axis"
      />
      <line
        :x1="chart.left"
        :y1="chart.top"
        :x2="chart.left"
        :y2="chart.bottom"
        class="opportunity-matrix__axis"
      />
      <line
        :x1="(chart.left + chart.right) / 2"
        :y1="chart.top"
        :x2="(chart.left + chart.right) / 2"
        :y2="chart.bottom"
        class="opportunity-matrix__gridline"
      />
      <line
        :x1="chart.left"
        :y1="(chart.top + chart.bottom) / 2"
        :x2="chart.right"
        :y2="(chart.top + chart.bottom) / 2"
        class="opportunity-matrix__gridline"
      />

      <text x="274" y="310" class="opportunity-matrix__axis-label">
        {{ xAxisLabel }}
      </text>
      <text
        x="16"
        y="145"
        class="opportunity-matrix__axis-label"
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
          class="opportunity-matrix__point opportunity-matrix__point--worth"
          data-opportunity-shape="circle"
          :data-status="point.status"
        />
        <polygon
          v-else-if="point.status === 'proceed-with-caution'"
          :points="diamondPoints(point)"
          class="opportunity-matrix__point opportunity-matrix__point--caution"
          data-opportunity-shape="diamond"
          :data-status="point.status"
        />
        <rect
          v-else-if="point.status === 'not-recommended-now'"
          :x="coordinate(point).x - 7"
          :y="coordinate(point).y - 7"
          width="14"
          height="14"
          class="opportunity-matrix__point opportunity-matrix__point--negative"
          data-opportunity-shape="square"
          :data-status="point.status"
        />
        <circle
          v-else
          :cx="coordinate(point).x"
          :cy="coordinate(point).y"
          r="7"
          class="opportunity-matrix__point opportunity-matrix__point--insufficient"
          data-opportunity-shape="hollow-circle"
          :data-status="point.status"
        />
      </g>
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
              <th scope="col">研究方向</th>
              <th scope="col">状态</th>
              <th scope="col">{{ xAxisLabel }}</th>
              <th scope="col">{{ yAxisLabel }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="point in points" :key="point.id">
              <th scope="row">{{ point.label }}</th>
              <td class="opportunity-matrix__status">
                <span
                  class="opportunity-matrix__legend-shape"
                  :data-shape="statusDefinitions[point.status].shape"
                  aria-hidden="true"
                />
                {{ statusDefinitions[point.status].label }}
              </td>
              <td>{{ point.x }}</td>
              <td>{{ point.y }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </details>
  </figure>
</template>

<style scoped>
.opportunity-matrix {
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

.opportunity-matrix__title {
  color: var(--color-text-strong);
  font-size: var(--text-lg-size);
  font-weight: 750;
  line-height: var(--text-lg-line);
}

.opportunity-matrix__summary {
  max-width: 68ch;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.opportunity-matrix__legend {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-4);
  margin: 0;
  padding: 0;
  color: var(--color-text);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
  list-style: none;
}

.opportunity-matrix__legend li,
.opportunity-matrix__status {
  display: inline-flex;
  gap: var(--space-2);
  align-items: center;
}

.opportunity-matrix__legend-shape {
  display: inline-block;
  flex: 0 0 auto;
  width: 10px;
  height: 10px;
  border: 2px solid var(--color-text-muted);
  background: transparent;
}

.opportunity-matrix__legend-shape[data-shape="circle"] {
  border-color: var(--color-positive);
  border-radius: 50%;
  background: var(--color-positive);
}

.opportunity-matrix__legend-shape[data-shape="diamond"] {
  border-color: var(--color-caution);
  background: var(--color-caution-bg);
  transform: rotate(45deg);
}

.opportunity-matrix__legend-shape[data-shape="square"] {
  border-color: var(--color-negative);
  background: var(--color-negative);
}

.opportunity-matrix__legend-shape[data-shape="hollow-circle"] {
  border-style: dashed;
  border-radius: 50%;
}

.opportunity-matrix__chart {
  display: none;
  width: 100%;
  min-height: 260px;
  overflow: visible;
}

.opportunity-matrix__mobile-list {
  display: grid;
  gap: var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}

.opportunity-matrix__mobile-list li {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--space-2) var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-left: 4px solid var(--color-border-control);
  border-radius: var(--radius-md);
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.opportunity-matrix__mobile-list strong {
  color: var(--color-text-strong);
}

.opportunity-matrix__axis {
  stroke: var(--color-border-control);
  stroke-width: 1.5;
}

.opportunity-matrix__gridline {
  stroke: var(--color-border);
  stroke-dasharray: 4 5;
  stroke-width: 1;
}

.opportunity-matrix__axis-label {
  fill: var(--color-text-muted);
  font-family: var(--font-ui);
  font-size: 12px;
  text-anchor: middle;
}

.opportunity-matrix__point {
  stroke-width: 2;
}

.opportunity-matrix__point--worth {
  fill: var(--color-positive);
  stroke: var(--color-positive);
}

.opportunity-matrix__point--caution {
  fill: var(--color-caution-bg);
  stroke: var(--color-caution);
}

.opportunity-matrix__point--negative {
  fill: var(--color-negative);
  stroke: var(--color-negative);
}

.opportunity-matrix__point--insufficient {
  fill: var(--color-surface);
  stroke: var(--color-text-muted);
  stroke-dasharray: 3 2;
}

@media (max-width: 767px) {
  .opportunity-matrix__legend {
    display: none;
  }
}

@media (min-width: 768px) {
  .opportunity-matrix__chart {
    display: block;
  }

  .opportunity-matrix__mobile-list {
    display: none;
  }
}
</style>
