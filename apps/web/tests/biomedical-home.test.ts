import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { beforeEach, describe, expect, it, vi } from "vitest"

import IndexPage from "../app/pages/index.vue"

const catalogApi = vi.hoisted(() => ({
  getHome: vi.fn(),
  getStats: vi.fn(),
  listPapers: vi.fn(),
}))

mockNuxtImport("useCatalogApi", () => () => catalogApi)

const generatedAt = "2026-07-17T08:30:00Z"
const known = <T>(value: T) => ({ state: "known" as const, value })
const analysis = {
  coverage_ratio: known(0.82),
  generated_at: generatedAt,
  missing_signals: [],
  sample_size: known(128),
  sources: ["PubMed", "Europe PMC"],
  window_days: 7,
}
const paper = {
  has_benchmark: known(false),
  has_code: known(false),
  has_data: known(true),
  id: "paper-1",
  journal: {
    id: "journal-1",
    slug: "journal-of-clinical-oncology",
    title: "Journal of Clinical Oncology",
  },
  publication_types: ["Journal Article"],
  published_at: known("2026-07-17T06:00:00Z"),
  status: "active",
  subjects: [
    {
      id: "subject-1",
      name: "Oncology",
      slug: "oncology",
    },
  ],
  title: "Prospective oncology cohort with external validation",
  type: known("research_article"),
}

const homeResponse = {
  active_journals: {
    analysis,
    items: [
      {
        journal: {
          id: "journal-1",
          jcr_metric_year: 2025,
          jif: known(45.3),
          slug: "journal-of-clinical-oncology",
          title: "Journal of Clinical Oncology",
        },
        paper_count: known(18),
        publication_change_ratio: known(0.2),
      },
    ],
  },
  catalog_generation: "catalog-2026-07-17",
  citation_momentum: {
    analysis: {
      ...analysis,
      sources: ["Europe PMC"],
      window_days: 30,
    },
    items: [
      {
        citation_delta: known(12),
        citations_per_day: known(0.4),
        cohort_percentile: known(0.91),
        paper,
      },
    ],
  },
  coverage: {
    analysis: {
      ...analysis,
      window_days: 30,
    },
    citation_coverage_ratio: known(0.76),
    jcr_metric_year: 2025,
    mesh_coverage_ratio: known(0.68),
    publication_type_coverage_ratio: known(0.94),
    taxonomy_version: "jcr-biomedical-2025-v1",
  },
  entity_momentum: {
    analysis: {
      ...analysis,
      missing_signals: ["target_baseline"],
      sample_size: known(74),
      window_days: 30,
    },
    items: [
      {
        entity_type: "disease",
        estimate: known(1.42),
        label: "Non-small-cell lung cancer",
      },
      {
        entity_type: "target",
        estimate: { state: "missing" as const },
        label: "EGFR",
      },
      {
        entity_type: "method",
        estimate: known(1.18),
        label: "Spatial transcriptomics",
      },
    ],
  },
  evidence_gaps: ["target_baseline"],
  generated_at: generatedAt,
  latest_papers: {
    analysis: {
      ...analysis,
      sample_size: known(1),
      window_days: 1,
    },
    items: [paper],
  },
  research_opportunities: {
    analysis: {
      ...analysis,
      sample_size: known(23),
      window_days: 90,
    },
    items: [
      {
        evidence_ids: ["paper-1"],
        formula_version: "opportunity-v2",
        id: "opportunity-1",
        missing_signals: ["external_validation"],
        status: "proceed_with_caution",
        summary: "增长信号明确，但外部验证仍不足。",
        title: "多中心外部验证",
      },
    ],
  },
  scope: {
    jcr_metric_year: 2025,
    taxonomy_version: "jcr-biomedical-2025-v1",
  },
  subject_momentum: {
    analysis,
    items: [
      {
        confidence_interval: {
          lower: 1.12,
          upper: 1.64,
        },
        estimate: known(1.36),
        independent_journal_count: known(9),
        independent_team_count: known(31),
        subject: {
          id: "subject-1",
          name: "Oncology",
          slug: "oncology",
        },
      },
    ],
  },
}

describe("biomedical intelligence home", () => {
  beforeEach(() => {
    catalogApi.getHome.mockReset()
    catalogApi.getStats.mockReset()
    catalogApi.listPapers.mockReset()
    catalogApi.getHome.mockResolvedValue(homeResponse)
  })

  it("loads one generation-bound home response and answers every required question", async () => {
    const wrapper = await mountSuspended(IndexPage)

    expect(catalogApi.getHome).toHaveBeenCalledTimes(1)
    expect(catalogApi.getStats).not.toHaveBeenCalled()
    expect(catalogApi.listPapers).not.toHaveBeenCalled()

    expect(wrapper.get("h1").text()).toBe(
      "医学生物学研究情报，从新论文到可验证趋势",
    )
    for (const heading of [
      "今日新增精选论文",
      "学科趋势",
      "引用增长",
      "活跃期刊",
      "热门疾病/靶点/方法",
      "研究机会",
      "数据覆盖",
    ]) {
      expect(wrapper.text()).toContain(heading)
    }

    expect(wrapper.text()).toContain("Journal of Clinical Oncology")
    expect(wrapper.text()).toContain("Oncology")
    expect(wrapper.text()).toContain("Journal Article")
    expect(wrapper.text()).toContain("JCR 指标年份")
    expect(wrapper.text()).toContain("2025")
    expect(wrapper.text()).toContain("jcr-biomedical-2025-v1")
  })

  it("shows window, generation time, sample, coverage and explicit missing state for every intelligence module", async () => {
    const wrapper = await mountSuspended(IndexPage)
    const modules = wrapper.findAll("[data-intelligence-module]")

    expect(modules).toHaveLength(7)
    for (const module of modules) {
      expect(module.find("[data-window]").exists()).toBe(true)
      expect(module.find("[data-generated-at]").exists()).toBe(true)
      expect(module.find("[data-sample-size]").exists()).toBe(true)
      expect(module.find("[data-coverage]").exists()).toBe(true)
      expect(module.find("[data-missing-state]").exists()).toBe(true)
    }

    const entityModule = wrapper.get(
      '[data-intelligence-module="entity-momentum"]',
    )
    expect(
      entityModule.get(
        '.entity-momentum__grid [data-value-state="missing"]',
      ).text(),
    ).toBe("缺失")
    expect(entityModule.text()).toContain("target_baseline")
    const missingEntity = entityModule.findAll("article").find((item) =>
      item.text().includes("EGFR"),
    )
    expect(missingEntity?.text()).toContain("缺失")
    expect(missingEntity?.text()).not.toContain("0%")
  })
})
