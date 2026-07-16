<script setup lang="ts">
import {
  presentDataValue,
  type DataValue,
} from "~/utils/dataValue"

type OpportunityStatus =
  | "insufficient-evidence"
  | "not-recommended-now"
  | "proceed-with-caution"
  | "worth-pursuing"

interface OpportunityPoint {
  id: string
  label: string
  missingSignals: string[]
  status: OpportunityStatus
  x: DataValue<number>
  y: DataValue<number>
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

const normalizedPoints = computed(() =>
  props.points.map((point) => ({
    ...point,
    // 缺失信号按调用方来源顺序复制，组件不排序也不改写原数组。
    missingSignals: [...point.missingSignals],
  })),
)

const statusOrder: OpportunityStatus[] = [
  "worth-pursuing",
  "proceed-with-caution",
  "not-recommended-now",
  "insufficient-evidence",
]

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

const groupedPoints = computed(() =>
  statusOrder.map((status) => ({
    definition: statusDefinitions[status],
    points: normalizedPoints.value.filter((point) => point.status === status),
    status,
  })),
)

const plottablePoints = computed(() =>
  normalizedPoints.value.flatMap((point) => {
    if (point.x.state !== "known" || point.y.state !== "known") {
      return []
    }
    return [
      {
        id: point.id,
        label: point.label,
        missingSignals: [...point.missingSignals],
        status: point.status,
        x: point.x.value,
        y: point.y.value,
      },
    ]
  }),
)

const unplottableCount = computed(
  () => normalizedPoints.value.length - plottablePoints.value.length,
)

const unavailableCoordinateState = computed<"insufficient" | "missing">(() =>
  normalizedPoints.value.some(
    (point) => point.x.state === "missing" || point.y.state === "missing",
  )
    ? "missing"
    : "insufficient",
)

function coordinateText(value: DataValue<number>) {
  return presentDataValue(value).text
}

function missingSignalsText(signals: string[]) {
  return signals.length > 0 ? signals.join("；") : "无"
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
        v-for="status in statusOrder"
        :key="status"
        :data-status="status"
      >
        <span
          class="opportunity-matrix__legend-shape"
          :data-shape="statusDefinitions[status].shape"
          aria-hidden="true"
        />
        {{ statusDefinitions[status].label }}
      </li>
    </ul>

    <DataState
      v-if="normalizedPoints.length === 0"
      state="empty"
      :title="emptyTitle"
      :message="emptyMessage"
    />

    <template v-else>
      <p
        v-if="unplottableCount > 0"
        class="opportunity-matrix__coverage-note"
        data-unplottable-count
      >
        {{ unplottableCount }} 个方向因坐标缺失或未覆盖而未绘制，仍保留在分组列表和数据表中。
      </p>

      <div
        class="opportunity-matrix__mobile-groups"
        aria-label="按推荐状态分组的研究机会"
      >
        <section
          v-for="group in groupedPoints"
          :key="group.status"
          :data-opportunity-group="group.status"
        >
          <h2>
            <span
              class="opportunity-matrix__legend-shape"
              :data-shape="group.definition.shape"
              aria-hidden="true"
            />
            {{ group.definition.label }}
          </h2>
          <ul v-if="group.points.length > 0">
            <li
              v-for="point in group.points"
              :key="point.id"
              :data-opportunity-id="point.id"
            >
              <strong>{{ point.label }}</strong>
              <dl>
                <div>
                  <dt>{{ xAxisLabel }}</dt>
                  <dd :data-value-state="point.x.state">
                    {{ coordinateText(point.x) }}
                  </dd>
                </div>
                <div>
                  <dt>{{ yAxisLabel }}</dt>
                  <dd :data-value-state="point.y.state">
                    {{ coordinateText(point.y) }}
                  </dd>
                </div>
                <div>
                  <dt>缺失信号</dt>
                  <dd data-missing-signals>
                    {{ missingSignalsText(point.missingSignals) }}
                  </dd>
                </div>
              </dl>
            </li>
          </ul>
          <p v-else>
            暂无
          </p>
        </section>
      </div>

      <details class="opportunity-matrix__overview data-table-disclosure">
        <summary>查看矩阵概览</summary>
        <OpportunityPlot
          v-if="plottablePoints.length > 0"
          class="opportunity-matrix__mobile-chart"
          :points="plottablePoints"
          :summary="summary"
          :x-axis-label="xAxisLabel"
          :y-axis-label="yAxisLabel"
        />
        <DataState
          v-else
          :state="unavailableCoordinateState"
          title="暂无可绘制坐标"
          message="所有方向的坐标均为缺失或未覆盖，矩阵不会推断或填补位置。"
        />
        <ul
          class="opportunity-matrix__point-details"
          aria-label="矩阵点详情"
        >
          <li
            v-for="point in normalizedPoints"
            :key="point.id"
            :data-opportunity-detail="point.id"
          >
            <strong>{{ point.label }}</strong>
            <span>{{ statusDefinitions[point.status].label }}</span>
            <dl>
              <div>
                <dt>{{ xAxisLabel }}</dt>
                <dd :data-value-state="point.x.state">
                  {{ coordinateText(point.x) }}
                </dd>
              </div>
              <div>
                <dt>{{ yAxisLabel }}</dt>
                <dd :data-value-state="point.y.state">
                  {{ coordinateText(point.y) }}
                </dd>
              </div>
              <div>
                <dt>缺失信号</dt>
                <dd data-missing-signals>
                  {{ missingSignalsText(point.missingSignals) }}
                </dd>
              </div>
            </dl>
          </li>
        </ul>
      </details>

      <OpportunityPlot
        v-if="plottablePoints.length > 0"
        class="opportunity-matrix__desktop-chart"
        :points="plottablePoints"
        :summary="summary"
        :x-axis-label="xAxisLabel"
        :y-axis-label="yAxisLabel"
      />
      <DataState
        v-else
        class="opportunity-matrix__desktop-state"
        :state="unavailableCoordinateState"
        title="暂无可绘制坐标"
        message="所有方向的坐标均为缺失或未覆盖，矩阵不会推断或填补位置。"
      />
      <ul
        class="opportunity-matrix__point-details opportunity-matrix__desktop-details"
        aria-label="矩阵点详情"
      >
        <li
          v-for="point in normalizedPoints"
          :key="point.id"
          :data-opportunity-detail="point.id"
        >
          <strong>{{ point.label }}</strong>
          <span>{{ statusDefinitions[point.status].label }}</span>
          <dl>
            <div>
              <dt>{{ xAxisLabel }}</dt>
              <dd :data-value-state="point.x.state">
                {{ coordinateText(point.x) }}
              </dd>
            </div>
            <div>
              <dt>{{ yAxisLabel }}</dt>
              <dd :data-value-state="point.y.state">
                {{ coordinateText(point.y) }}
              </dd>
            </div>
            <div>
              <dt>缺失信号</dt>
              <dd data-missing-signals>
                {{ missingSignalsText(point.missingSignals) }}
              </dd>
            </div>
          </dl>
        </li>
      </ul>
    </template>

    <details class="opportunity-matrix__table data-table-disclosure">
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
              <th scope="col">缺失信号</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="point in normalizedPoints" :key="point.id">
              <th scope="row">{{ point.label }}</th>
              <td class="opportunity-matrix__status">
                <span
                  class="opportunity-matrix__legend-shape"
                  :data-shape="statusDefinitions[point.status].shape"
                  aria-hidden="true"
                />
                {{ statusDefinitions[point.status].label }}
              </td>
              <td
                data-coordinate-value
                :data-value-state="point.x.state"
              >
                {{ coordinateText(point.x) }}
              </td>
              <td
                data-coordinate-value
                :data-value-state="point.y.state"
              >
                {{ coordinateText(point.y) }}
              </td>
              <td data-missing-signals>
                {{ missingSignalsText(point.missingSignals) }}
              </td>
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
  display: none;
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

.opportunity-matrix__coverage-note {
  margin: 0;
  padding: var(--space-3);
  border-left: 4px solid var(--color-caution);
  background: var(--color-caution-bg);
  color: var(--color-text);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.opportunity-matrix__mobile-groups {
  display: grid;
  gap: var(--space-4);
}

.opportunity-matrix__mobile-groups > section {
  display: grid;
  gap: var(--space-2);
}

.opportunity-matrix__mobile-groups h2 {
  display: flex;
  gap: var(--space-2);
  align-items: center;
  margin: 0;
  color: var(--color-text-strong);
  font-size: var(--text-base-size);
  line-height: var(--text-base-line);
}

.opportunity-matrix__mobile-groups ul {
  display: grid;
  gap: var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}

.opportunity-matrix__mobile-groups li {
  display: grid;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-left: 4px solid var(--color-border-control);
  border-radius: var(--radius-md);
  background: var(--color-surface-subtle);
}

.opportunity-matrix__mobile-groups strong {
  color: var(--color-text-strong);
}

.opportunity-matrix__mobile-groups dl {
  display: grid;
  gap: var(--space-1);
  margin: 0;
}

.opportunity-matrix__mobile-groups dl > div {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--space-3);
}

.opportunity-matrix__mobile-groups dt,
.opportunity-matrix__mobile-groups dd,
.opportunity-matrix__mobile-groups > section > p {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.opportunity-matrix__mobile-groups dd {
  font-variant-numeric: tabular-nums;
}

.opportunity-matrix__point-details {
  display: grid;
  gap: var(--space-3);
  margin: var(--space-4) 0 0;
  padding: 0;
  list-style: none;
}

.opportunity-matrix__point-details > li {
  display: grid;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface-subtle);
}

.opportunity-matrix__point-details strong {
  color: var(--color-text-strong);
}

.opportunity-matrix__point-details > li > span {
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.opportunity-matrix__point-details dl {
  display: grid;
  gap: var(--space-1);
  margin: 0;
}

.opportunity-matrix__point-details dl > div {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, auto);
  gap: var(--space-3);
}

.opportunity-matrix__point-details dt,
.opportunity-matrix__point-details dd {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.opportunity-matrix__point-details dd {
  text-align: right;
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-style: italic;
}

.opportunity-matrix__overview {
  display: block;
}

.opportunity-matrix__desktop-chart,
.opportunity-matrix__desktop-details,
.opportunity-matrix__desktop-state {
  display: none;
}

@media (min-width: 768px) {
  .opportunity-matrix__legend {
    display: flex;
  }

  .opportunity-matrix__mobile-groups,
  .opportunity-matrix__overview {
    display: none;
  }

  .opportunity-matrix__desktop-chart {
    display: block;
  }

  .opportunity-matrix__desktop-state {
    display: grid;
  }

  .opportunity-matrix__desktop-details {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(208px, 1fr));
    margin-top: 0;
  }
}
</style>
