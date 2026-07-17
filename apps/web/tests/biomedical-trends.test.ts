import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { beforeEach, describe, expect, it, vi } from "vitest"

import TrendsPage from "../app/pages/trends.vue"

const catalogApi = vi.hoisted(() => ({
  getHome: vi.fn(),
  listMethodTrends: vi.fn(),
  listPaperTrends: vi.fn(),
  listTopicTrends: vi.fn(),
}))

mockNuxtImport("useCatalogApi", () => () => catalogApi)

const generatedAt = "2026-07-17T08:30:00Z"
const known = <T>(value: T) => ({ state: "known" as const, value })
const analysis = {
  analysis_run_id: "11111111-1111-4111-8111-111111111111",
  analysis_type: "biomedical_momentum",
  baseline_window_days: 30,
  coverage_ratio: known(0.82),
  formula_version: known("biomedical-momentum-v1"),
  cohort_revision: "a".repeat(64),
  generated_at: generatedAt,
  missing_signals: [],
  recent_window_days: 7,
  sample_size: known(128),
  sources: ["PubMed", "Europe PMC"],
  window_days: 30,
}
const homeResponse = {
  citation_momentum: {
    analysis: {
      ...analysis,
      formula_version: known("citation-velocity-v1"),
      sources: ["Europe PMC"],
    },
    items: [],
  },
  entity_momentum: {
    analysis: {
      ...analysis,
      formula_version: known("entity-momentum-v1"),
      missing_signals: ["target_baseline"],
    },
    items: [
      {
        adjusted_p_value: known(0.021),
        baseline_count: known(118),
        confidence_interval: {
          lower: 1.08,
          upper: 1.42,
        },
        entity_type: "target",
        estimate: known(1.21),
        independent_journal_count: known(7),
        independent_team_count: known(19),
        label: "EGFR",
        model_family: "poisson",
        p_value: known(0.011),
        recent_count: known(29),
      },
    ],
  },
  subject_momentum: {
    analysis,
    items: [
      {
        adjusted_p_value: known(0.019),
        baseline_count: known(132),
        confidence_interval: {
          lower: 1.12,
          upper: 1.64,
        },
        estimate: known(1.36),
        independent_journal_count: known(9),
        independent_team_count: known(31),
        label: "论文发表率比",
        model_family: "negative_binomial",
        p_value: known(0.009),
        recent_count: known(41),
      },
    ],
  },
}
const legacyTrendResponse = {
  generated_at: generatedAt,
  items: [],
  window_days: 30,
}

describe("biomedical trends overview", () => {
  beforeEach(() => {
    catalogApi.getHome.mockReset()
    catalogApi.listMethodTrends.mockReset()
    catalogApi.listPaperTrends.mockReset()
    catalogApi.listTopicTrends.mockReset()

    catalogApi.getHome.mockResolvedValue(homeResponse)
    catalogApi.listMethodTrends.mockResolvedValue(legacyTrendResponse)
    catalogApi.listPaperTrends.mockResolvedValue(legacyTrendResponse)
    catalogApi.listTopicTrends.mockResolvedValue(legacyTrendResponse)
  })

  it("loads only the generation-bound home snapshot and renders the three biomedical trend modules", async () => {
    const wrapper = await mountSuspended(TrendsPage, {
      route: "/trends?window_days=90",
    })

    expect(catalogApi.getHome).toHaveBeenCalledTimes(1)
    expect(catalogApi.listPaperTrends).not.toHaveBeenCalled()
    expect(catalogApi.listTopicTrends).not.toHaveBeenCalled()
    expect(catalogApi.listMethodTrends).not.toHaveBeenCalled()

    expect(wrapper.findAll("h1")).toHaveLength(1)
    expect(wrapper.get("h1").text()).toBe("医学生物学趋势总览")
    expect(wrapper.get(
      '[data-intelligence-module="subject-momentum"]',
    ).text()).toContain("学科趋势")
    expect(wrapper.get(
      '[data-intelligence-module="citation-momentum"]',
    ).text()).toContain("引用增长")
    expect(wrapper.get(
      '[data-intelligence-module="entity-momentum"]',
    ).text()).toContain("热门疾病/靶点/方法")
    expect(wrapper.text()).toContain("negative_binomial")
    expect(wrapper.text()).toContain("poisson")
    expect(wrapper.text()).toContain("1.12–1.64")

    expect(wrapper.find('[name="window_days"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain("排名窗口")
    expect(wrapper.text()).not.toContain("Topic 趋势")
    expect(wrapper.text()).not.toContain("Method 趋势")
  })

  it("keeps API-declared empty states and analysis metadata visible without client inference", async () => {
    const wrapper = await mountSuspended(TrendsPage)
    const modules = wrapper.findAll("[data-intelligence-module]")

    expect(modules).toHaveLength(3)
    for (const module of modules) {
      expect(module.find("[data-window]").exists()).toBe(true)
      expect(module.find("[data-generated-at]").exists()).toBe(true)
      expect(module.find("[data-formula-version]").exists()).toBe(true)
      expect(module.find("[data-missing-state]").exists()).toBe(true)
    }

    expect(
      wrapper.get('[data-intelligence-module="citation-momentum"]')
        .find('[data-state="empty"]')
        .exists(),
    ).toBe(true)
    expect(wrapper.text()).toContain("至少需要两个来源快照；页面不会用引用总量代替增量。")
    expect(wrapper.text()).toContain("target_baseline")
  })
})
