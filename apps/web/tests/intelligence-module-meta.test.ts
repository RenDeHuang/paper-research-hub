import { mountSuspended } from "@nuxt/test-utils/runtime"
import { describe, expect, it } from "vitest"

import IntelligenceModuleMeta from "../app/components/IntelligenceModuleMeta.vue"
import type { AnalysisMetadata } from "../app/types/biomedical"

const baseAnalysis: Omit<AnalysisMetadata, "formula_version"> = {
  analysis_run_id: "11111111-1111-4111-8111-111111111111",
  analysis_type: "biomedical_momentum",
  baseline_window_days: 30,
  coverage_ratio: { state: "known", value: 0.84 },
  cohort_revision: "a".repeat(64),
  generated_at: { state: "known", value: "2026-07-17T08:30:00Z" },
  missing_signals: [],
  recent_window_days: 7,
  sample_size: { state: "known", value: 96 },
  sources: { state: "known", value: ["PubMed", "JCR"] },
  window_days: { state: "known", value: 30 },
}

describe("IntelligenceModuleMeta", () => {
  it.each([
    [
      { state: "known" as const, value: "subject-momentum/v2" },
      "known",
      "subject-momentum/v2",
    ],
    [{ state: "missing" as const }, "missing", "缺失"],
    [undefined, "unknown", "公式版本字段未覆盖"],
  ])(
    "renders formula_version %s as %s",
    async (formulaVersion, expectedState, expectedText) => {
      const wrapper = await mountSuspended(IntelligenceModuleMeta, {
        props: {
          analysis: {
            ...baseAnalysis,
            formula_version: formulaVersion,
          },
        },
      })
      const value = wrapper.get("[data-formula-version]")

      expect(value.attributes("data-value-state")).toBe(expectedState)
      expect(value.text()).toBe(expectedText)
    },
  )

  it("shows analysis identity and window provenance when present", async () => {
    const wrapper = await mountSuspended(IntelligenceModuleMeta, {
      props: {
        analysis: {
          ...baseAnalysis,
          formula_version: { state: "known", value: "subject-momentum/v2" },
        },
      },
    })

    expect(wrapper.get("[data-analysis-run-id]").text()).toBe(
      "11111111-1111-4111-8111-111111111111",
    )
    expect(wrapper.get("[data-analysis-type]").text()).toBe(
      "biomedical_momentum",
    )
    expect(wrapper.get("[data-cohort-revision]").text()).toBe("a".repeat(64))
    expect(wrapper.get("[data-recent-window-days]").text()).toBe("7 天")
    expect(wrapper.get("[data-baseline-window-days]").text()).toBe("30 天")
  })
})
