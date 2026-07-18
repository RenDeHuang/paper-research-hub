import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { beforeEach, describe, expect, it, vi } from "vitest"

import OpportunitiesPage from "../app/pages/opportunities.vue"

const catalogApi = vi.hoisted(() => ({
  listResearchOpportunities: vi.fn(),
}))

mockNuxtImport("useCatalogApi", () => () => catalogApi)

const generatedAt = "2026-07-17T08:30:00Z"
const analysisRunID = "11111111-1111-4111-8111-111111111111"
const known = <T>(value: T) => ({ state: "known" as const, value })
const analysis = {
  analysis_run_id: analysisRunID,
  analysis_type: "research_opportunity",
  baseline_window_days: 90,
  coverage_ratio: known(0.84),
  cohort_revision: "a".repeat(64),
  generated_at: generatedAt,
  missing_signals: [],
  recent_window_days: 30,
  sample_size: known(24),
  sources: ["PubMed", "Europe PMC"],
  window_days: 30,
}

const opportunitiesResponse = {
  analysis,
  items: [
    {
      analysis_run_id: analysisRunID,
      coverage_ratio: known(0.74),
      estimates: [
        {
          confidence_interval: {
            lower: 1.12,
            upper: 1.64,
          },
          metric: "trend_rate_ratio",
          value: 1.36,
        },
        {
          confidence_interval: {
            lower: 0.12,
            upper: 0.24,
          },
          metric: "external_validation_share",
          value: 0.18,
        },
      ],
      formula_version: "opportunity-v2",
      generated_at: generatedAt,
      id: "opportunity-1",
      limitations: ["外部验证仍不足"],
      missing_signals: ["external_validation"],
      recommended_next_steps: ["补充多中心验证", "扩大独立队列"],
      status: "proceed_with_caution",
      supporting_work_ids: ["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"],
      summary: "增长信号明确，但外部验证仍不足。",
      target_id: "subject-1",
      target_kind: "subject",
      title: "多中心外部验证",
      trigger_rule: {
        code: "single_center_external_validation_gap",
        version: "biomedical-opportunities/v1",
      },
    },
  ],
  pagination: {
    has_more: false,
    limit: 20,
    next_cursor: null,
    total: 1,
  },
}

describe("research opportunities page", () => {
  beforeEach(() => {
    catalogApi.listResearchOpportunities.mockReset()
    catalogApi.listResearchOpportunities.mockResolvedValue(
      opportunitiesResponse,
    )
  })

  it("renders rule-driven cards, analysis metadata, estimates, coverage and supporting work IDs", async () => {
    const wrapper = await mountSuspended(OpportunitiesPage, {
      route: "/opportunities?status=proceed_with_caution",
    })

    expect(catalogApi.listResearchOpportunities).toHaveBeenCalledWith({
      status: "proceed_with_caution",
    })
    expect(wrapper.get("h1").text()).toBe("研究机会")
    expect(wrapper.text()).toContain("single_center_external_validation_gap")
    expect(wrapper.text()).toContain("trend_rate_ratio")
    expect(wrapper.text()).toContain("1.12–1.64")
    expect(wrapper.text()).toContain("74%")
    expect(wrapper.text()).toContain("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
    expect(wrapper.text()).not.toContain("竞争密度")
  })
})
