import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { beforeEach, describe, expect, it, vi } from "vitest"

import IndexPage from "../app/pages/index.vue"
import type { HomeResponse } from "../app/types/biomedical"

const catalogApi = vi.hoisted(() => ({
  getHome: vi.fn(),
  getStats: vi.fn(),
  listPapers: vi.fn(),
}))

mockNuxtImport("useCatalogApi", () => () => catalogApi)

const generatedAt = "2026-07-17T08:30:00Z"
const analysisRunID = "11111111-1111-4111-8111-111111111111"
const paperID = "00000000-0000-4000-8000-000000000001"
const journalID = "00000000-0000-4000-8000-000000000101"
const subjectID = "00000000-0000-4000-8000-000000000201"
const venueMetricSnapshotID = "00000000-0000-4000-8000-000000000301"
const journalSubjectMetricID = "00000000-0000-4000-8000-000000000302"
const subjectVersionID = "00000000-0000-4000-8000-000000000303"
const subjectRuleID = "00000000-0000-4000-8000-000000000304"
const eligibilityRevisionID = "00000000-0000-4000-8000-000000000305"
const known = <T>(value: T) => ({ state: "known" as const, value })
const analysis = {
  analysis_run_id: analysisRunID,
  analysis_type: "subject_trend",
  baseline_window_days: 30,
  coverage_ratio: known(0.82),
  cohort_revision: "a".repeat(64),
  generated_at: known(generatedAt),
  missing_signals: [],
  recent_window_days: 7,
  sample_size: known(128),
  sources: known(["PubMed", "Europe PMC"]),
  window_days: known(7),
}
const jcrAssessment = known({
  assessed_at: generatedAt,
  decision: "accepted" as const,
  evidence: {
    matches: [
      {
        jcr_category: "ONCOLOGY",
        journal_subject_metric_id: journalSubjectMetricID,
        metric_year: 2025,
        subject_id: subjectID,
        subject_rule_id: subjectRuleID,
        subject_slug: "oncology",
        subject_version_id: subjectVersionID,
        venue_id: journalID,
        venue_metric_snapshot_id: venueMetricSnapshotID,
      },
    ],
    metric_year: 2025,
    metrics: [
      {
        jcr_category: "ONCOLOGY",
        metric_year: 2025,
        venue_id: journalID,
        venue_metric_snapshot_id: venueMetricSnapshotID,
      },
    ],
    policy_version: "journal-jif-or-q1/v1",
    subject_version_id: subjectVersionID,
    subject_version_key: "jcr-biomedical-2025-v1",
    venue: {
      issn_l: "0732-183X",
      venue_id: journalID,
    },
  },
  id: eligibilityRevisionID,
  metric_year: 2025,
  policy_version: "journal-jif-or-q1/v1",
  subject_version_id: subjectVersionID,
  subject_version_key: "jcr-biomedical-2025-v1",
})
const paper: HomeResponse["latest_papers"]["items"][number] = {
  article_usage: { state: "missing" as const },
  citation_count: known(27),
  citation_percentile: { state: "missing" as const },
  citation_snapshots: { state: "missing" as const },
  citation_source: known("Europe PMC"),
  citation_velocity: { state: "missing" as const },
  has_benchmark: known(false),
  has_code: known(false),
  has_data: known(true),
  id: paperID,
  jcr_assessment: jcrAssessment,
  journal: {
    id: journalID,
    slug: "journal-of-clinical-oncology",
    title: "Journal of Clinical Oncology",
  },
  mesh_headings: { state: "missing" as const },
  open_fulltext: { state: "missing" as const },
  publication_types: ["Journal Article"],
  publication_types_state: known(["Journal Article"]),
  published_at: known("2026-07-17T06:00:00Z"),
  status: "active",
  subjects: [
    {
      id: subjectID,
      name: "Oncology",
      slug: "oncology",
    },
  ],
  title: "Prospective oncology cohort with external validation",
  type: known("research_article"),
}
const provenance = {
  normalized_assertion_id: "00000000-0000-4000-8000-000000000501",
  projection_assertion_id: "00000000-0000-4000-8000-000000000502",
  source: "pubmed" as const,
  source_path: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
  source_record_id: "00000000-0000-4000-8000-000000000503",
  status_raw: "ppublish",
}
const publicationItem = (
  kind:
    | "accepted"
    | "ahead_of_print"
    | "electronic_published"
    | "print_published",
  date: string,
  status: string,
) => ({
  event: {
    date,
    date_precision: "day" as const,
    kind,
    provenance: {
      ...provenance,
      status_raw: status,
    },
    publication_model: "Print-Electronic",
    publication_status: status,
  },
  paper,
})
const snapshotPagination = (total: number) => ({
  has_more: false as const,
  limit: total,
  next_cursor: null,
  total,
})

const homeResponse = {
  active_journals: {
    analysis,
    items: [
      {
        journal: {
          id: journalID,
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
  catalog_generation: "00000000-0000-4000-8000-000000000701",
  citation_momentum: {
    analysis: {
      ...analysis,
      sources: known(["Europe PMC"]),
      window_days: known(30),
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
      window_days: known(30),
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
      window_days: known(7),
    },
    items: [
      {
        adjusted_p_value: known(0.022),
        baseline_count: known(118),
        confidence_interval: {
          lower: 1.05,
          upper: 1.31,
        },
        estimate: known(1.18),
        entity_type: "disease",
        independent_journal_count: known(7),
        independent_team_count: known(19),
        label: "Non-small-cell lung cancer",
        model_family: "poisson",
        p_value: known(0.014),
        recent_count: known(29),
      },
      {
        adjusted_p_value: known(0.031),
        baseline_count: known(102),
        confidence_interval: {
          lower: 0.98,
          upper: 1.21,
        },
        entity_type: "target",
        estimate: known(1.08),
        independent_journal_count: known(8),
        independent_team_count: { state: "missing" as const },
        label: "EGFR",
        model_family: "negative_binomial",
        p_value: known(0.019),
        recent_count: known(26),
      },
    ],
  },
  evidence_gaps: ["target_baseline"],
  generated_at: generatedAt,
  latest_papers: {
    analysis: {
      ...analysis,
      sample_size: known(1),
      window_days: known(1),
    },
    items: [paper],
    pagination: snapshotPagination(1),
  },
  publication_updates: {
    calendar_date: "2026-07-17",
    calendar_timezone: "UTC" as const,
    formal_publications_today: {
      analysis: {
        ...analysis,
        sample_size: known(1),
        window_days: known(1),
      },
      items: [
        publicationItem("print_published", "2026-07-17", "ppublish"),
      ],
      pagination: snapshotPagination(1),
    },
    recent_acceptances: {
      analysis: {
        ...analysis,
        sample_size: known(1),
        window_days: known(7),
      },
      items: [
        publicationItem("accepted", "2026-07-16", "accepted"),
      ],
      pagination: snapshotPagination(1),
    },
    recent_online_first: {
      analysis: {
        ...analysis,
        sample_size: known(1),
        window_days: known(7),
      },
      items: [
        publicationItem("ahead_of_print", "2026-07-15", "aheadofprint"),
      ],
      pagination: snapshotPagination(1),
    },
  },
  research_opportunities: {
    analysis: {
      ...analysis,
      sample_size: known(23),
      window_days: { state: "missing" as const },
    },
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
        id: "00000000-0000-4000-8000-000000000601",
        limitations: ["外部验证仍不足"],
        missing_signals: ["external_validation"],
        recommended_next_steps: [
          "补充多中心验证",
          "扩大独立队列",
        ],
        status: "proceed_with_caution",
        supporting_work_ids: [paperID],
        summary: "增长信号明确，但外部验证仍不足。",
        target_id: subjectID,
        target_kind: "subject",
        title: "多中心外部验证",
        trigger_rule: {
          code: "single_center_external_validation_gap",
          version: "biomedical-opportunities/v1",
        },
      },
    ],
  },
  scope: {
    jcr_metric_year: 2025,
    taxonomy_version: "jcr-biomedical-2025-v1",
  },
  snapshot_schema: "home-snapshot/v2" as const,
  subject_momentum: {
    analysis,
    items: [
      {
        adjusted_p_value: known(0.015),
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
        subject: {
          id: subjectID,
          name: "Oncology",
          slug: "oncology",
        },
      },
    ],
  },
} satisfies HomeResponse

describe("biomedical intelligence home", () => {
  beforeEach(() => {
    catalogApi.getHome.mockReset()
    catalogApi.getStats.mockReset()
    catalogApi.listPapers.mockReset()
    catalogApi.getHome.mockResolvedValue(homeResponse)
  })

  it("loads one generation-bound response in the approved daily dashboard order", async () => {
    const wrapper = await mountSuspended(IndexPage)

    expect(catalogApi.getHome).toHaveBeenCalledTimes(1)
    expect(catalogApi.getStats).not.toHaveBeenCalled()
    expect(catalogApi.listPapers).not.toHaveBeenCalled()

    expect(wrapper.get("h1").text()).toBe("medpaperhub 今日论文情报")
    for (const heading of [
      "今日正式发表",
      "近期接收",
      "在线优先",
      "最近 7 天趋势",
      "期刊动态",
      "学科动态",
    ]) {
      expect(wrapper.text()).toContain(heading)
    }

    expect(
      wrapper
        .findAll("[data-home-section]")
        .map((section) => section.attributes("data-home-section")),
    ).toEqual([
      "formal-publications",
      "recent-acceptances",
      "recent-online-first",
      "trends",
      "journals",
      "subjects",
    ])
    expect(wrapper.text()).toContain("Journal of Clinical Oncology")
    expect(wrapper.text()).toContain("Oncology")
    expect(wrapper.text()).toContain("Journal Article")
    expect(wrapper.text()).toContain("印刷正式发表")
    expect(wrapper.text()).toContain("已接收")
    expect(wrapper.text()).toContain("在线优先")
    expect(wrapper.text()).toContain("Q1 / JIF ≥ 10")
    expect(wrapper.text()).toContain("2025")
    expect(wrapper.text()).not.toContain("Catalog generation")
    expect(wrapper.text()).not.toContain("研究机会")
    expect(wrapper.text()).not.toContain("数据覆盖")
    expect(wrapper.find('form[role="search"]').exists()).toBe(false)
  })

  it("keeps status compact and does not expose internal generation or analysis IDs", async () => {
    const wrapper = await mountSuspended(IndexPage)
    const status = wrapper.get(".sync-status")

    expect(status.text()).toContain("2026-07-17")
    expect(status.text()).toContain("UTC")
    expect(status.text()).toContain("JCR 2025")
    expect(status.text()).toContain("Q1 / JIF ≥ 10")
    expect(wrapper.text()).not.toContain("catalog-2026-07-17")
    expect(wrapper.text()).not.toContain(analysisRunID)
    expect(wrapper.text()).not.toContain("jcr-biomedical-2025-v1")
    expect(wrapper.find("[data-window]").exists()).toBe(false)
    expect(wrapper.find("[data-generated-at]").exists()).toBe(false)
  })
})
