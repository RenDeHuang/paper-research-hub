import type { Component } from "vue"

import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { beforeEach, describe, expect, it, vi } from "vitest"

const catalogApi = vi.hoisted(() => ({
  getJournal: vi.fn(),
  listJournals: vi.fn(),
}))
const useHead = vi.hoisted(() => vi.fn())

mockNuxtImport("useCatalogApi", () => () => catalogApi)
mockNuxtImport("useHead", () => useHead)

const journalPages = import.meta.glob<{ default: Component }>(
  "../app/pages/journals/*.vue",
)
const generatedAt = "2026-07-17T08:30:00Z"
const known = <T>(value: T) => ({ state: "known" as const, value })
const analysis = {
  analysis_run_id: "11111111-1111-4111-8111-111111111111",
  analysis_type: "journal_editorial_pattern",
  baseline_window_days: 90,
  coverage_ratio: known(0.88),
  cohort_revision: "a".repeat(64),
  generated_at: generatedAt,
  missing_signals: [],
  recent_window_days: 90,
  sample_size: known(112),
  sources: ["PubMed", "JCR"],
  window_days: 90,
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
  aliases: ["J Clin Oncol"],
  categories: [
    {
      name: "ONCOLOGY",
      quartile: "Q1",
    },
    {
      name: "MEDICINE, GENERAL & INTERNAL",
      quartile: "Q1",
    },
  ],
  curation: {
    assessed_at: generatedAt,
    decision: "accepted",
    evidence: [
      {
        label: "ONCOLOGY",
        value: "Q1",
      },
      {
        label: "JIF",
        value: "45.3",
      },
    ],
    matched_rules: ["jcr_q1"],
    metric_year: 2025,
    policy_name: "biomedical-journal-admission",
    policy_version: 3,
  },
    editorial_patterns: {
    analysis,
    items: [
      {
        adjusted_p_value: known(0.012),
        baseline: "同 JCR Category、同 90 天窗口期刊",
        field_baseline_count: known(86),
        confidence_interval: {
          lower: 1.24,
          upper: 2.31,
        },
        coverage_ratio: known(0.86),
        dimension: "MeSH",
        estimate: known(1.69),
        estimate_kind: "odds_ratio",
        interpretation_kind: "editorial_pattern",
        label: "Lung Neoplasms",
        minimum_support_count: known(18),
        p_value: known(0.008),
        support_papers: known(28),
      },
    ],
  },
  evidence_gaps: ["author_country_coverage"],
  id: "journal-1",
  issn_l: known("0732-183X"),
  issns: ["0732-183X"],
  eissn: known("1527-7755"),
  jcr_metric_year: 2025,
  jif: known(45.3),
  paper_count: known(112),
  publisher: known("Wolters Kluwer"),
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
      next_cursor: "journal-next",
      total: 1,
    },
  },
  slug: "journal-of-clinical-oncology",
  taxonomy_version: "jcr-biomedical-2025-v1",
  title: "Journal of Clinical Oncology",
}
const journalListResponse = {
  analysis,
  catalog_generation: "catalog-2026-07-17",
  items: [journal],
  jcr_metric_year: 2025,
  pagination: {
    has_more: false,
    limit: 20,
    next_cursor: null,
    total: 1,
  },
  taxonomy_version: "jcr-biomedical-2025-v1",
}

async function loadJournalPage(file: "index.vue" | "[slug].vue") {
  const path = `../app/pages/journals/${file}`
  const loader = journalPages[path]

  expect(loader, `${path} must exist`).toBeDefined()
  return (await loader!()).default
}

describe("journal intelligence pages", () => {
  beforeEach(() => {
    useHead.mockReset()
    catalogApi.getJournal.mockReset()
    catalogApi.listJournals.mockReset()
    catalogApi.getJournal.mockResolvedValue(journal)
    catalogApi.listJournals.mockResolvedValue(journalListResponse)
  })

  it("loads the journal list with JCR year, taxonomy, sample, window and missing state", async () => {
    const JournalListPage = await loadJournalPage("index.vue")
    const wrapper = await mountSuspended(JournalListPage, {
      route: "/journals?cursor=journal-page-2",
    })

    expect(catalogApi.listJournals).toHaveBeenCalledWith({
      cursor: "journal-page-2",
    })
    expect(wrapper.get("h1").text()).toBe("期刊")
    expect(wrapper.text()).toContain("Journal of Clinical Oncology")
    const headInput = useHead.mock.calls.at(-1)?.[0]
    const description = headInput?.meta?.find(
      (item: { name?: string }) => item.name === "description",
    )?.content
    expect(description).toContain("全部 JCR Q1")
    expect(description).not.toContain("JIF 不低于 10")
    expect(wrapper.text()).toContain("JCR 指标年份")
    expect(wrapper.text()).toContain("taxonomy version")
    expect(wrapper.text()).toContain("jcr-biomedical-2025-v1")
    expect(wrapper.get("[data-window]").exists()).toBe(true)
    expect(wrapper.get("[data-sample-size]").exists()).toBe(true)
    expect(wrapper.get("[data-coverage]").exists()).toBe(true)
    expect(wrapper.get("[data-missing-state]").exists()).toBe(true)
  })

  it("renders identity, curation evidence, recent papers and non-causal editorial estimates from one journal detail response", async () => {
    const JournalDetailPage = await loadJournalPage("[slug].vue")
    const wrapper = await mountSuspended(JournalDetailPage, {
      route: "/journals/journal-of-clinical-oncology?cursor=journal-paper-page-2",
    })

    expect(catalogApi.getJournal).toHaveBeenCalledWith(
      "journal-of-clinical-oncology",
      {
        cursor: "journal-paper-page-2",
      },
    )
    expect(wrapper.get("h1").text()).toBe("Journal of Clinical Oncology")
    for (const label of [
      "Publisher",
      "ISSN-L",
      "ISSN",
      "eISSN",
      "JCR 指标年份",
      "JIF",
      "全部 JCR Category / Quartile",
      "命中的准入规则",
      "最近论文",
      "编辑与选题模式估计",
      "同 JCR Category、同 90 天窗口期刊",
      "editorial_pattern",
      "支持论文数",
      "覆盖率",
      "证据缺口",
      "领域基线数",
      "最低支持数",
    ]) {
      expect(wrapper.text()).toContain(label)
    }
    expect(wrapper.text()).toContain("ONCOLOGY · Q1")
    expect(wrapper.text()).toContain("1.24–2.31")
    expect(wrapper.text()).toContain("统计关联不表示因果偏好")
    expect(wrapper.text()).not.toMatch(/期刊(?:喜欢|偏爱)|journal likes/i)
    expect(wrapper.get('a[href*="cursor=journal-next"]').text()).toBe("下一页")
  })
})
