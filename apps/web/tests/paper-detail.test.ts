import type { Component } from "vue"

import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { clearNuxtData } from "#imports"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { formatCatalogDate } from "../app/utils/catalogPresentation"

const catalogApi = vi.hoisted(() => ({
  getPaper: vi.fn(),
}))

mockNuxtImport("useCatalogApi", () => () => catalogApi)

const paperPages = import.meta.glob<{ default: Component }>(
  "../app/pages/papers/*.vue",
)
const known = <T>(value: T) => ({ state: "known" as const, value })
const analysisRunID = "11111111-1111-4111-8111-111111111111"
const currentSnapshotID = "22222222-2222-4222-8222-222222222222"
const baselineSnapshotID = "33333333-3333-4333-8333-333333333333"
const sourceRecordID = "44444444-4444-4444-8444-444444444444"
const ingestionJobID = "55555555-5555-4555-8555-555555555555"
const subjectVersionID = "66666666-6666-4666-8666-666666666666"
const subjectID = "77777777-7777-4777-8777-777777777777"
const publicationTypeID = "88888888-8888-4888-8888-888888888888"
const supportWorkID = "99999999-9999-4999-8999-999999999999"
const observedAt = "2026-07-16T08:00:00Z"
const retrievedAt = "2026-07-16T08:05:00Z"
const asOf = "2026-07-17T08:00:00Z"
const generatedAt = "2026-07-17T08:30:00Z"
const sourceRevision = "a".repeat(64)
const paper = {
  abstract: known("A source-bound citation analysis."),
  canonical_key: "doi:10.1000/citation-evidence",
  citation_analysis_evidence: known({
    analysis_run_id: analysisRunID,
    as_of: asOf,
    formula_version: "citation-intelligence/v1",
    generated_at: generatedAt,
    percentile: {
      citation_snapshot_id: currentSnapshotID,
      cohort_key: "oncology|2026|randomized-controlled-trial",
      cohort_size: 42,
      midrank: 36.5,
      minimum_cohort_size: 20,
      publication_type_id: publicationTypeID,
      publication_year: 2026,
      state: "known" as const,
      subject_id: subjectID,
      subject_version_id: subjectVersionID,
      supporting_work_ids: [supportWorkID],
    },
    source: "openalex",
    source_revision: sourceRevision,
    velocity: {
      baseline_snapshot_id: baselineSnapshotID,
      current_snapshot_id: currentSnapshotID,
      elapsed_days: 30.5,
      state: "known" as const,
      window_days: 30,
    },
  }),
  citation_count: known(42),
  citation_percentile: known(86.9),
  citation_snapshots: known([
    {
      count: 42,
      coverage: 1,
      dataset_version: `openalex-source-record/${sourceRecordID}`,
      definition_version: "openalex-cited-by-count/v1",
      ingestion_job_id: ingestionJobID,
      observed_at: observedAt,
      retrieved_at: retrievedAt,
      source: "openalex",
      source_record_id: sourceRecordID,
    },
  ]),
  citation_source: known("openalex"),
  citation_velocity: known(1.18),
  curation: {
    state: "missing" as const,
  },
  article_usage: {
    state: "missing" as const,
  },
  has_benchmark: known(false),
  has_code: known(false),
  has_data: known(true),
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  mesh_headings: known([
    {
      descriptor_ui: "D008175",
      is_major_topic: true,
      label: "Lung Neoplasms",
      qualifiers: [
        {
          is_major_topic: false,
          label: "therapy",
          qualifier_ui: "Q000401",
          source_path: "/PubmedArticle/MeshHeading[1]/QualifierName[1]",
        },
      ],
      source_path: "/PubmedArticle/MeshHeading[1]/DescriptorName",
    },
  ]),
  publication_types: ["Randomized Controlled Trial"],
  publication_types_state: known(["Randomized Controlled Trial"]),
  published_at: known("2026-07-15T06:00:00Z"),
  source_provenance: known([]),
  status: "active",
  title: "Source-bound citation intelligence",
  type: known("research_article"),
}

async function loadPaperDetailPage() {
  const path = "../app/pages/papers/[id].vue"
  const loader = paperPages[path]

  expect(loader, `${path} must exist`).toBeDefined()
  return (await loader!()).default
}

describe("paper citation evidence", () => {
  beforeEach(async () => {
    await clearNuxtData("paper-detail")
    catalogApi.getPaper.mockReset()
    catalogApi.getPaper.mockResolvedValue(paper)
  })

  it("renders source snapshots and formal analysis-run evidence", async () => {
    const PaperDetailPage = await loadPaperDetailPage()
    const wrapper = await mountSuspended(PaperDetailPage, {
      route: `/papers/${paper.id}`,
    })

    expect(catalogApi.getPaper).toHaveBeenCalledWith(paper.id)
    for (const label of [
      "引用证据",
      "引用来源",
      "引用速度",
      "同 cohort 百分位",
      "观测时间",
      "抓取时间",
      "定义版本",
      "数据集版本",
      "分析 run ID",
      "公式版本",
      "速度窗口",
      "真实间隔",
      "cohort key",
      "cohort 大小",
      "最低 cohort 大小",
      "source revision",
    ]) {
      expect(wrapper.text()).toContain(label)
    }
    for (const value of [
      "openalex",
      formatCatalogDate(observedAt),
      formatCatalogDate(retrievedAt),
      "openalex-cited-by-count/v1",
      `openalex-source-record/${sourceRecordID}`,
      analysisRunID,
      "citation-intelligence/v1",
      "30 天",
      "30.5 天",
      "oncology|2026|randomized-controlled-trial",
      sourceRevision,
    ]) {
      expect(wrapper.text()).toContain(value)
    }
  })

  it("uses the API paper title as the page h1", async () => {
    const PaperDetailPage = await loadPaperDetailPage()
    const wrapper = await mountSuspended(PaperDetailPage, {
      route: `/papers/${paper.id}`,
    })

    expect(wrapper.findAll("h1")).toHaveLength(1)
    expect(wrapper.get("h1").text()).toBe(paper.title)
  })

  it("renders complete MeSH and Publication Type evidence", async () => {
    const PaperDetailPage = await loadPaperDetailPage()
    const wrapper = await mountSuspended(PaperDetailPage, {
      route: `/papers/${paper.id}`,
    })

    const panel = wrapper.get("[data-medical-evidence]")
    for (const label of [
      "医学主题证据",
      "MeSH Descriptor",
      "Descriptor UI",
      "Qualifier",
      "Qualifier UI",
      "Major Topic",
      "source_path",
      "Publication Type",
    ]) {
      expect(panel.text()).toContain(label)
    }
    for (const value of [
      "Lung Neoplasms",
      "D008175",
      "therapy",
      "Q000401",
      "/PubmedArticle/MeshHeading[1]/DescriptorName",
      "/PubmedArticle/MeshHeading[1]/QualifierName[1]",
      "Randomized Controlled Trial",
    ]) {
      expect(panel.text()).toContain(value)
    }
    expect(panel.findAll("[data-major-topic='true']")).toHaveLength(1)
    expect(panel.findAll("[data-major-topic='false']")).toHaveLength(1)
  })

  it("shows article-level downloads and usage as explicitly missing", async () => {
    const PaperDetailPage = await loadPaperDetailPage()
    const wrapper = await mountSuspended(PaperDetailPage, {
      route: `/papers/${paper.id}`,
    })

    const usage = wrapper.get("[data-article-usage]")
    expect(usage.attributes("data-value-state")).toBe("missing")
    expect(usage.text()).toContain("下载/使用量")
    expect(usage.text()).toContain("缺失")
    expect(usage.text()).toContain("暂无权威来源")
    expect(usage.text()).not.toContain("42")
    expect(usage.text()).not.toContain("全文")
  })

  it("does not derive unavailable analysis values from citation snapshots", async () => {
    catalogApi.getPaper.mockResolvedValue({
      ...paper,
      citation_analysis_evidence: undefined,
      citation_percentile: {
        state: "insufficient_evidence",
        reason: "cohort size is below the declared minimum",
      },
      citation_velocity: {
        state: "missing",
      },
    })

    const PaperDetailPage = await loadPaperDetailPage()
    const wrapper = await mountSuspended(PaperDetailPage, {
      route: `/papers/${paper.id}`,
    })

    expect(
      wrapper.get("[data-citation-velocity]").attributes("data-value-state"),
    ).toBe("missing")
    expect(
      wrapper.get("[data-citation-percentile]").attributes("data-value-state"),
    ).toBe("insufficient_evidence")
    expect(
      wrapper.get("[data-citation-analysis-evidence]").attributes("data-state"),
    ).toBe("unknown")
    expect(wrapper.get("[data-citation-velocity]").text()).toBe("缺失")
    expect(wrapper.get("[data-citation-percentile]").text()).toContain(
      "证据不足",
    )
    expect(wrapper.text()).not.toContain("0 次/天")
    expect(wrapper.get("[data-citation-percentile]").text()).not.toBe("0%")
  })
})
