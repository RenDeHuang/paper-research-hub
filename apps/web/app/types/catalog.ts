export type UUID = string

export type CatalogValue<T> =
  | T
  | {
      state: "known"
      value: T
    }
  | {
      state: "missing"
    }
  | {
      state: "unknown"
    }

export type PaperType =
  | "research_article"
  | "review"
  | "preprint"
  | "dataset"
  | "benchmark"

export type PaperLifecycleStatus =
  | "active"
  | "withdrawn"
  | "retracted"
  | "rejected"
  | "superseded"

export type SourceName =
  | "crossref"
  | "arxiv"
  | "openreview"
  | "s2"
  | "pubmed"
  | "pmc"
  | "openalex"
  | "springer_nature"
  | "elsevier"
  | "manual"

export type PaperSort =
  | "relevance"
  | "published_at_desc"
  | "citations_desc"
  | "trend_desc"

export type ResearchOpportunityStatus =
  | "worth_pursuing"
  | "proceed_with_caution"
  | "not_recommended_now"
  | "insufficient_evidence"

export interface ProblemDetails {
  type: string
  title: string
  status: number
  code: string
  detail: string
  instance: string
  request_id: string
}

export interface Pagination {
  limit: number
  total?: number
  next_cursor: string | null
  has_more: boolean
}

export interface FacetBucket {
  key: string
  label: string
  count: number
}

export interface Facets {
  paper_types: FacetBucket[]
  topics: FacetBucket[]
  methods: FacetBucket[]
  statuses: FacetBucket[]
  sources: FacetBucket[]
}

export interface TaxonomyReference {
  id: UUID
  slug: string
  name: string
}

export interface JournalReference {
  id: UUID
  slug: string
  title: string
}

export interface TaxonomyItem extends TaxonomyReference {
  description: CatalogValue<string>
  paper_count: number
  paper_ids?: UUID[]
  created_at?: string
  updated_at?: string
}

export interface AuthorReference {
  id: UUID
  name?: string
  display_name?: string
  orcid?: string
  institution_id?: UUID
  institution_name?: string
  position?: number
  is_corresponding?: boolean
}

export interface SourceProvenance {
  source: SourceName | string
  event_key?: string
  source_record_id: UUID | string
  source_time?: string
  source_url?: string
  ingested_at?: string
  last_seen_at?: string
  normalization_policy_version?: string
  scope_policy_version?: string
  projection_policy_version?: string
}

export interface CurationPayload {
  decision: "accepted" | "rejected" | "not_applicable"
  matched_rules: unknown
  evidence: unknown
  metric_year: number
  policy_name: string
  policy_version: number
  assessed_at: string
}

export interface RankingCoverage {
  eligible_count?: number
  ranked_count?: number
  coverage_ratio?: CatalogValue<number>
  citation_signal_ratio?: CatalogValue<number>
  code_signal_ratio?: CatalogValue<number>
  data_signal_ratio?: CatalogValue<number>
  benchmark_signal_ratio?: CatalogValue<number>
  state?: "known" | "missing" | "unknown"
  value?: number
}

export interface MissingSignal {
  signal: string
  reason?: string
  affected_entity_ids?: UUID[]
}

export interface RankingMetadata {
  formula_version?: string
  window_days?: number
  generated_at?: string
  coverage?: RankingCoverage
  missing_signals: Array<MissingSignal | string>
  confidence?: CatalogValue<number>
}

export interface PaperSummary {
  id: UUID
  canonical_key?: string
  title: string
  status: PaperLifecycleStatus | string
  published_at: CatalogValue<string>
  type: CatalogValue<PaperType>
  abstract?: CatalogValue<string>
  abstract_snippet?: CatalogValue<string>
  authors?: AuthorReference[]
  topics?: Array<TaxonomyReference | string>
  methods?: Array<TaxonomyReference | string>
  source_provenance?: Array<SourceProvenance | string>
  has_code: CatalogValue<boolean>
  has_data: CatalogValue<boolean>
  has_benchmark: CatalogValue<boolean>
  citation_count?: CatalogValue<number>
  trend_score?: CatalogValue<number>
  curation?: CatalogValue<CurationPayload>
  ranking?: RankingMetadata
  journal?: JournalReference
  publication_types?: string[]
  subjects?: TaxonomyReference[]
}

export interface PaperDetail extends PaperSummary {
  institutions?: unknown[]
  external_identifiers?: unknown[]
  datasets?: unknown[]
  benchmarks?: unknown[]
  models?: unknown[]
  code_repositories?: unknown[]
  metric_snapshots?: unknown[]
  created_at?: string
  updated_at?: string
}

export interface StatsResponse {
  generated_at: string
  formula_version?: string
  papers_total: CatalogValue<number>
  papers_last_7_days: CatalogValue<number>
  topics_total: CatalogValue<number>
  methods_total: CatalogValue<number>
  with_code_ratio: CatalogValue<number>
  with_data_ratio: CatalogValue<number>
  with_benchmark_ratio: CatalogValue<number>
}

export interface PaperListResponse {
  items: PaperSummary[]
  pagination: Pagination
  facets: Facets
}

export interface TaxonomyListResponse {
  items: TaxonomyItem[]
  pagination: Pagination
}

export interface TrendItem {
  rank: number
  score: number
  rank_change?: number
  subject_id?: UUID
  paper?: PaperSummary
  topic?: TaxonomyItem
  method?: TaxonomyItem
  ranking: RankingMetadata
}

export interface TrendListResponse {
  generated_at: string
  window_days: number
  items: TrendItem[]
  pagination: Pagination
}

export interface ResearchOpportunity {
  id: UUID
  title: string
  summary?: string
  status: ResearchOpportunityStatus
  evidence_ids: UUID[]
  growth_score?: CatalogValue<number>
  competition_density?: CatalogValue<number>
  data_availability?: CatalogValue<number>
  reproducibility?: CatalogValue<number>
  missing_signals?: string[]
  limitations?: string[]
  formula_version: string
  generated_at?: string
  evaluated_at?: string
  window_start?: string
  window_end?: string
  metrics?: unknown[]
  ranking?: RankingMetadata
  recommended_next_steps?: string[]
}

export interface ResearchOpportunityListResponse {
  generated_at: string
  formula_version: string
  items: ResearchOpportunity[]
  pagination: Pagination
}

export interface PaperListQuery {
  q?: string
  published_from?: string
  published_to?: string
  type?: PaperType
  topic?: string
  method?: string
  has_code?: boolean
  has_data?: boolean
  has_benchmark?: boolean
  status?: PaperLifecycleStatus
  source?: SourceName
  sort?: PaperSort
  limit?: number
  cursor?: string
}

export interface PageQuery {
  limit?: number
  cursor?: string
}

export interface TrendQuery extends PageQuery {
  window_days?: number
}

export interface OpportunityQuery extends PageQuery {
  status?: ResearchOpportunityStatus
}

export type CatalogQuery = Record<string, boolean | number | string>
