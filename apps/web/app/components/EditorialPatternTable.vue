<script setup lang="ts">
import type {
  AnalysisCollection,
  EditorialPatternEstimate,
} from "~/types/biomedical"
import {
  catalogDataValue,
  percentageDataValue,
} from "~/utils/catalogPresentation"
import { presentDataValue } from "~/utils/dataValue"

defineProps<{
  patterns: AnalysisCollection<EditorialPatternEstimate>
}>()

const numberFormatter = new Intl.NumberFormat("zh-CN", {
  maximumFractionDigits: 3,
})

function estimateText(item: EditorialPatternEstimate) {
  const value = catalogDataValue(item.estimate)
  if (value.state !== "known") {
    return presentDataValue(value).text
  }
  return `${numberFormatter.format(value.value)}×`
}

function intervalText(interval: EditorialPatternEstimate["confidence_interval"]) {
  return `${numberFormatter.format(interval.lower)}–${numberFormatter.format(interval.upper)}`
}
</script>

<template>
  <section class="editorial-patterns" aria-labelledby="editorial-patterns-heading">
    <div class="section-heading">
      <div>
        <p class="section-kicker">
          相对于预先声明基线
        </p>
        <h2 id="editorial-patterns-heading">
          编辑与选题模式估计
        </h2>
      </div>
    </div>

    <p class="editorial-patterns__boundary">
      统计关联不表示因果偏好；结果仅描述当前窗口内相对于同领域基线的富集或发表率差异。
    </p>

    <IntelligenceModuleMeta :analysis="patterns.analysis" />

    <DataState
      v-if="patterns.items.length === 0"
      state="empty"
      title="当前没有可发布的模式估计"
      message="API 已成功返回空集合；样本不足或缺少基线时不会生成肯定结论。"
    />
    <div v-else class="data-table-scroll" tabindex="0">
      <table>
        <caption class="visually-hidden">
          期刊相对于同 JCR Category 基线的编辑与选题模式估计
        </caption>
        <thead>
          <tr>
            <th scope="col">维度</th>
            <th scope="col">实体</th>
            <th scope="col">解释</th>
            <th scope="col">估计值</th>
            <th scope="col">95% 区间</th>
            <th scope="col">p 值</th>
            <th scope="col">校正 p 值</th>
            <th scope="col">领域基线</th>
            <th scope="col">领域基线数</th>
            <th scope="col">最低支持数</th>
            <th scope="col">支持论文数</th>
            <th scope="col">覆盖率</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in patterns.items" :key="`${item.dimension}:${item.label}`">
            <td>{{ item.dimension }}</td>
            <th scope="row">{{ item.label }}</th>
            <td>{{ item.interpretation_kind }}</td>
            <td :data-value-state="catalogDataValue(item.estimate).state">
              {{ estimateText(item) }}
            </td>
            <td>{{ intervalText(item.confidence_interval) }}</td>
            <td :data-value-state="catalogDataValue(item.p_value).state">
              {{ presentDataValue(catalogDataValue(item.p_value)).text }}
            </td>
            <td :data-value-state="catalogDataValue(item.adjusted_p_value).state">
              {{ presentDataValue(catalogDataValue(item.adjusted_p_value)).text }}
            </td>
            <td>{{ item.baseline }}</td>
            <td :data-value-state="catalogDataValue(item.field_baseline_count).state">
              {{ presentDataValue(catalogDataValue(item.field_baseline_count)).text }}
            </td>
            <td :data-value-state="catalogDataValue(item.minimum_support_count).state">
              {{ presentDataValue(catalogDataValue(item.minimum_support_count)).text }}
            </td>
            <td :data-value-state="catalogDataValue(item.support_papers).state">
              {{ presentDataValue(catalogDataValue(item.support_papers)).text }}
            </td>
            <td :data-value-state="percentageDataValue(item.coverage_ratio).state">
              {{ presentDataValue(percentageDataValue(item.coverage_ratio)).text }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.editorial-patterns {
  display: grid;
  gap: var(--space-4);
}

.editorial-patterns__boundary {
  margin: 0;
  padding: var(--space-3);
  border-left: 4px solid var(--color-info);
  background: var(--color-info-bg);
  color: var(--color-text);
}

[data-value-state="missing"],
[data-value-state="unknown"] {
  color: var(--color-text-muted);
  font-family: var(--font-ui);
  font-style: italic;
}
</style>
