import { createServer } from "node:http"

import type {
  HomeResponse,
  PublicationUpdateCollection,
  PublicationUpdateItem,
} from "../app/types/biomedical"

const host = "127.0.0.1"
const port = 3200
const scenarios = ["default", "empty", "error"] as const
type Scenario = (typeof scenarios)[number]
const generatedAt = "2026-07-18T08:30:00Z"
const catalogGeneration = "00000000-0000-4000-8000-000000000701"
const requestID = "00000000-0000-4000-8000-000000000901"
const analysisRunID = "11111111-1111-4111-8111-111111111111"
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
  cohort_revision: "a".repeat(64),
  coverage_ratio: known(0.82),
  generated_at: known(generatedAt),
  missing_signals: [],
  recent_window_days: 7,
  sample_size: known(128),
  sources: known(["PubMed"]),
  window_days: known(7),
}
const analysisForWindow = (windowDays: number) => ({
  ...analysis,
  window_days: known(windowDays),
})
const subject = {
  id: "00000000-0000-4000-8000-000000000201",
  name: "Oncology",
  slug: "oncology",
}
const journalReference = {
  id: "00000000-0000-4000-8000-000000000101",
  slug: "journal-of-clinical-oncology",
  title: "Journal of Clinical Oncology",
}
const journalSummary = {
  id: "00000000-0000-4000-8000-000000000101",
  jcr_metric_year: 2025,
  jif: known(45.3),
  slug: "journal-of-clinical-oncology",
  title: "Journal of Clinical Oncology",
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
        subject_id: subject.id,
        subject_rule_id: subjectRuleID,
        subject_slug: subject.slug,
        subject_version_id: subjectVersionID,
        venue_id: journalReference.id,
        venue_metric_snapshot_id: venueMetricSnapshotID,
      },
    ],
    metric_year: 2025,
    metrics: [
      {
        jcr_category: "ONCOLOGY",
        metric_year: 2025,
        venue_id: journalReference.id,
        venue_metric_snapshot_id: venueMetricSnapshotID,
      },
    ],
    policy_version: "journal-all-q1/v2",
    subject_version_id: subjectVersionID,
    subject_version_key: "jcr-biomedical-2025-v1",
    venue: {
      issn_l: "0732-183X",
      venue_id: journalReference.id,
    },
  },
  id: eligibilityRevisionID,
  metric_year: 2025,
  policy_version: "journal-all-q1/v2",
  subject_version_id: subjectVersionID,
  subject_version_key: "jcr-biomedical-2025-v1",
})
const paper: HomeResponse["latest_papers"]["items"][number] = {
  analysis_ready: false,
  methods_state: "not_ready",
  citation_count: known(27),
  has_benchmark: known(false),
  has_code: known(false),
  has_data: known(true),
  id: "00000000-0000-4000-8000-000000000001",
  journal: journalReference,
  publication_types: ["Journal Article"],
  publication_types_state: known(["Journal Article"]),
  published_at: known("2026-07-18T06:00:00Z"),
  status: "active",
  subjects: [subject],
  title: "Prospective oncology cohort with external validation",
  topics_state: "not_ready",
  type: known("research_article"),
  article_usage: { state: "missing" as const },
  citation_percentile: { state: "missing" as const },
  citation_snapshots: { state: "missing" as const },
  citation_source: known("Europe PMC"),
  citation_velocity: { state: "missing" as const },
  jcr_assessment: jcrAssessment,
  mesh_headings: { state: "missing" as const },
  official_link: {
    content_channel: "journal_published",
    expires_at: "2026-07-20T06:00:00Z",
    link_role: "official_article",
    policy_version: "official-url/v1",
    url: "https://publisher.example.test/articles/prospective-oncology-cohort",
    verification_id: "00000000-0000-4000-8000-000000000504",
    verified_at: "2026-07-19T06:00:00Z",
    verifier_version: "official-url-verifier/v1",
  },
  open_fulltext: { state: "missing" as const },
  publicly_visible: true,
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
  kind: "accepted" | "ahead_of_print" | "print_published",
  date: string,
  status: string,
): PublicationUpdateItem => ({
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
const snapshotPagination = (
  total: number,
): PublicationUpdateCollection["pagination"] => ({
  has_more: false as const,
  limit: total,
  next_cursor: null,
  total,
})
const publicationCollection = (
  item: PublicationUpdateItem,
  windowDays: number,
): PublicationUpdateCollection => ({
  analysis: {
    ...analysis,
    sample_size: known(1),
    window_days: known(windowDays),
  },
  items: [item],
  pagination: snapshotPagination(1),
})

const homeResponse = {
  active_journals: {
    analysis,
    items: [
      {
        journal: journalSummary,
        paper_count: known(18),
        publication_change_ratio: known(0.2),
      },
    ],
  },
  catalog_generation: catalogGeneration,
  citation_momentum: {
    analysis: analysisForWindow(30),
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
    analysis: analysisForWindow(30),
    citation_coverage_ratio: known(0.76),
    jcr_metric_year: 2025,
    mesh_coverage_ratio: known(0.68),
    publication_type_coverage_ratio: known(0.94),
    taxonomy_version: "jcr-biomedical-2025-v1",
  },
  entity_momentum: {
    analysis,
    items: [
      {
        adjusted_p_value: known(0.022),
        baseline_count: known(118),
        confidence_interval: {
          lower: 1.05,
          upper: 1.31,
        },
        entity_type: "disease",
        estimate: known(1.18),
        independent_journal_count: known(7),
        independent_team_count: known(19),
        label: "Non-small-cell lung cancer",
        model_family: "poisson",
        p_value: known(0.014),
        recent_count: known(29),
      },
    ],
  },
  evidence_gaps: [],
  generated_at: generatedAt,
  latest_papers: {
    analysis,
    items: [paper],
    pagination: snapshotPagination(1),
  },
  publication_updates: {
    calendar_date: "2026-07-18",
    calendar_timezone: "UTC",
    formal_publications_today: publicationCollection(
      publicationItem("print_published", "2026-07-18", "ppublish"),
      1,
    ),
    recent_acceptances: publicationCollection(
      publicationItem("accepted", "2026-07-17", "accepted"),
      7,
    ),
    recent_online_first: publicationCollection(
      publicationItem("ahead_of_print", "2026-07-16", "aheadofprint"),
      7,
    ),
  },
  research_opportunities: {
    analysis: {
      ...analysis,
      sample_size: known(0),
      window_days: { state: "missing" as const },
    },
    items: [],
  },
  scope: {
    jcr_metric_year: 2025,
    taxonomy_version: "jcr-biomedical-2025-v1",
  },
  snapshot_schema: "home-snapshot/v2",
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
        independent_journal_count: known(9),
        independent_team_count: known(31),
        model_family: "negative_binomial",
        p_value: known(0.009),
        recent_count: known(41),
        subject,
      },
    ],
  },
} satisfies HomeResponse

const server = createServer((request, response) => {
  const requestURL = new URL(request.url ?? "/", `http://${host}:${port}`)
  const scopedRoute = parseScopedRoute(requestURL.pathname)
  const origin = request.headers.origin
  if (origin) {
    response.setHeader("Access-Control-Allow-Origin", origin)
    response.setHeader("Vary", "Origin")
  }
  response.setHeader("Access-Control-Allow-Headers", "Content-Type")
  response.setHeader("Access-Control-Allow-Methods", "GET, OPTIONS")
  response.setHeader("X-Request-ID", requestID)

  if (request.method === "OPTIONS") {
    response.writeHead(204)
    response.end()
    return
  }

  response.setHeader("Content-Type", "application/json; charset=utf-8")
  if (requestURL.pathname === "/health") {
    response.writeHead(200)
    response.end(JSON.stringify({ service: "catalog-e2e-mock", status: "ok" }))
    return
  }
  if (
    request.method === "GET"
    && scopedRoute?.scenario === "default"
    && scopedRoute.apiPath === "/api/v1/home"
  ) {
    response.setHeader("X-Catalog-Generation", catalogGeneration)
    response.writeHead(200)
    response.end(JSON.stringify(homeResponse))
    return
  }
  if (
    request.method === "GET"
    && scopedRoute?.scenario === "empty"
    && scopedRoute.apiPath === "/api/v1/home"
  ) {
    response.writeHead(503)
    response.end(JSON.stringify({
      code: "catalog_not_published",
      detail: "The deterministic E2E catalog has not been published.",
      instance: requestURL.pathname,
      request_id: requestID,
      status: 503,
      title: "Catalog not published",
      type: "about:blank",
    }))
    return
  }
  if (
    request.method === "GET"
    && scopedRoute?.scenario === "error"
    && scopedRoute.apiPath === "/api/v1/home"
  ) {
    response.writeHead(500)
    response.end(JSON.stringify({
      code: "internal_error",
      detail: "The deterministic E2E server returned the requested failure.",
      instance: requestURL.pathname,
      request_id: requestID,
      status: 500,
      title: "Internal server error",
      type: "about:blank",
    }))
    return
  }

  response.writeHead(503)
  response.end(JSON.stringify({
    code: "catalog_not_published",
    detail: "The deterministic E2E server publishes only the Home snapshot.",
    instance: requestURL.pathname,
    request_id: requestID,
    status: 503,
    title: "Catalog not published",
    type: "about:blank",
  }))
})

function parseScopedRoute(pathname: string): {
  apiPath: string
  scenario: Scenario
} | undefined {
  for (const scenario of scenarios) {
    const prefix = `/${scenario}`
    if (pathname.startsWith(`${prefix}/`)) {
      return {
        apiPath: pathname.slice(prefix.length),
        scenario,
      }
    }
  }
  return undefined
}

server.listen(port, host, () => {
  process.stdout.write(`Catalog E2E mock listening on http://${host}:${port}\n`)
})

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    server.close(() => process.exit(0))
  })
}
