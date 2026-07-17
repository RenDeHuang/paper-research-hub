import type { Component } from "vue"

import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { beforeEach, describe, expect, it, vi } from "vitest"

const catalogApi = vi.hoisted(() => ({
  getSubject: vi.fn(),
  listSubjects: vi.fn(),
}))

mockNuxtImport("useCatalogApi", () => () => catalogApi)

const subjectPages = import.meta.glob<{ default: Component }>(
  "../app/pages/subjects/*.vue",
)
const generatedAt = "2026-07-17T08:30:00Z"
const known = <T>(value: T) => ({ state: "known" as const, value })
const analysis = {
  analysis_run_id: "11111111-1111-4111-8111-111111111111",
  analysis_type: "subject_trend",
  baseline_window_days: 30,
  coverage_ratio: known(0.84),
  cohort_revision: "a".repeat(64),
  generated_at: generatedAt,
  missing_signals: [],
  recent_window_days: 7,
  sample_size: known(96),
  sources: ["PubMed", "JCR"],
  window_days: 30,
}
const paper = {
  has_benchmark: known(false),
  has_code: known(false),
  has_data: known(true),
  id: "paper-1",
  published_at: known("2026-07-17T06:00:00Z"),
  status: "active",
  title: "Prospective oncology cohort",
  type: known("research_article"),
}
const journal = {
  id: "journal-1",
  jcr_metric_year: 2025,
  jif: known(45.3),
  slug: "journal-of-clinical-oncology",
  title: "Journal of Clinical Oncology",
}
const subject = {
  active_journals: {
    analysis,
    items: [
      {
        journal,
        paper_count: known(18),
        publication_change_ratio: known(0.2),
      },
    ],
  },
  description: known("Versioned JCR oncology category."),
  evidence_gaps: ["target_baseline"],
  id: "subject-1",
  jcr_metric_year: 2025,
  journal_count: known(24),
  mesh_distribution: {
    analysis,
    items: [
      {
        count: known(26),
        label: "Lung Neoplasms",
        ratio: known(0.27),
      },
    ],
  },
  name: "Oncology",
  paper_count: known(96),
  publication_type_distribution: {
    analysis,
    items: [
      {
        count: known(42),
        label: "Randomized Controlled Trial",
        ratio: known(0.44),
      },
    ],
  },
  recent_papers: {
    analysis: {
      ...analysis,
      sample_size: known(1),
      window_days: 7,
    },
    items: [paper],
    pagination: {
      has_more: true,
      limit: 20,
      next_cursor: "subject-next",
    },
  },
  slug: "oncology",
  taxonomy_version: "jcr-biomedical-2025-v1",
  trend_estimates: {
    analysis,
    items: [
      {
        adjusted_p_value: known(0.018),
        baseline_count: known(132),
        confidence_interval: {
          lower: 1.12,
          upper: 1.64,
        },
        estimate: known(1.36),
        model_family: "negative_binomial",
        p_value: known(0.009),
        recent_count: known(41),
        independent_journal_count: known(9),
        independent_team_count: known(31),
        label: "论文发表率比",
      },
    ],
  },
}
const subjectListResponse = {
  analysis,
  catalog_generation: "catalog-2026-07-17",
  items: [subject],
  jcr_metric_year: 2025,
  pagination: {
    has_more: false,
    limit: 20,
    next_cursor: null,
  },
  taxonomy_version: "jcr-biomedical-2025-v1",
}

async function loadSubjectPage(file: "index.vue" | "[slug].vue") {
  const path = `../app/pages/subjects/${file}`
  const loader = subjectPages[path]

  expect(loader, `${path} must exist`).toBeDefined()
  return (await loader!()).default
}

describe("Subject intelligence pages", () => {
  beforeEach(() => {
    catalogApi.getSubject.mockReset()
    catalogApi.listSubjects.mockReset()
    catalogApi.getSubject.mockResolvedValue(subject)
    catalogApi.listSubjects.mockResolvedValue(subjectListResponse)
  })

  it("loads the Subject list from the URL-addressable generation-bound endpoint", async () => {
    const SubjectListPage = await loadSubjectPage("index.vue")
    const wrapper = await mountSuspended(SubjectListPage, {
      route: "/subjects?cursor=subject-page-2",
    })

    expect(catalogApi.listSubjects).toHaveBeenCalledWith({
      cursor: "subject-page-2",
    })
    expect(wrapper.get("h1").text()).toBe("学科")
    expect(wrapper.text()).toContain("Oncology")
    expect(wrapper.text()).toContain("JCR 指标年份")
    expect(wrapper.text()).toContain("taxonomy version")
    expect(wrapper.text()).toContain("jcr-biomedical-2025-v1")
    expect(wrapper.get("[data-window]").exists()).toBe(true)
    expect(wrapper.get("[data-sample-size]").exists()).toBe(true)
    expect(wrapper.get("[data-coverage]").exists()).toBe(true)
    expect(wrapper.get("[data-missing-state]").exists()).toBe(true)
  })

  it("renders recent papers, active journals, distributions, uncertainty and evidence gaps from one Subject detail response", async () => {
    const SubjectDetailPage = await loadSubjectPage("[slug].vue")
    const wrapper = await mountSuspended(SubjectDetailPage, {
      route: "/subjects/oncology?cursor=subject-paper-page-2",
    })

    expect(catalogApi.getSubject).toHaveBeenCalledWith("oncology", {
      cursor: "subject-paper-page-2",
    })
    for (const heading of [
      "最近论文",
      "活跃期刊",
      "MeSH 分布",
      "Publication Type 分布",
      "趋势估计与不确定性",
      "证据缺口",
    ]) {
      expect(wrapper.text()).toContain(heading)
    }
    expect(wrapper.text()).toContain("1.12–1.64")
    expect(wrapper.text()).toContain("negative_binomial")
    expect(wrapper.text()).toContain("41")
    expect(wrapper.text()).toContain("31")
    expect(wrapper.text()).toContain("target_baseline")
    expect(wrapper.text()).toContain("jcr-biomedical-2025-v1")
    expect(wrapper.get('a[href*="cursor=subject-next"]').text()).toBe("下一页")
  })
})
