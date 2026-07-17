import type {
  CatalogValue,
  JournalReference,
  MissingSignal,
  Pagination,
  PaperSummary,
  ResearchOpportunity,
  TaxonomyReference,
} from "~/types/catalog"

/**
 * Temporary structural boundary for Task 19/20.
 *
 * Task 17 can replace API return aliases with generated OpenAPI types without
 * coupling page components to generator namespaces. These shapes contain only
 * fields rendered by the biomedical intelligence UI.
 */
export interface AnalysisMetadata {
  coverage_ratio: CatalogValue<number>
  formula_version?: CatalogValue<string>
  generated_at: CatalogValue<string>
  missing_signals: Array<MissingSignal | string>
  sample_size: CatalogValue<number>
  sources: CatalogValue<string[]>
  window_days: CatalogValue<number>
}

export interface AnalysisCollection<T> {
  analysis: AnalysisMetadata
  items: T[]
}

export interface ConfidenceInterval {
  lower: number
  upper: number
}

export interface SubjectTrendEstimate {
  confidence_interval: ConfidenceInterval
  estimate: CatalogValue<number>
  independent_journal_count: CatalogValue<number>
  independent_team_count: CatalogValue<number>
  label?: string
  subject?: TaxonomyReference
}

export interface CitationMomentumItem {
  citation_delta: CatalogValue<number>
  citations_per_day: CatalogValue<number>
  cohort_percentile: CatalogValue<number>
  paper: PaperSummary
}

export interface JournalMetricCategory {
  name: string
  quartile: string
}

export interface JournalSummary extends JournalReference {
  aliases?: string[]
  categories?: JournalMetricCategory[]
  eissn?: CatalogValue<string>
  issn_l?: CatalogValue<string>
  issns?: string[]
  jcr_metric_year: number
  jif: CatalogValue<number>
  paper_count?: CatalogValue<number>
  publisher?: CatalogValue<string>
  taxonomy_version?: string
}

export interface JournalActivityItem {
  journal: JournalSummary
  paper_count: CatalogValue<number>
  publication_change_ratio: CatalogValue<number>
}

export type BiomedicalEntityType =
  | "disease"
  | "method"
  | "publication_type"
  | "study_design"
  | "target"

export interface EntityMomentumItem {
  entity_type: BiomedicalEntityType
  estimate: CatalogValue<number>
  label: string
}

export interface HomeCoverage {
  analysis: AnalysisMetadata
  citation_coverage_ratio: CatalogValue<number>
  jcr_metric_year: number
  mesh_coverage_ratio: CatalogValue<number>
  publication_type_coverage_ratio: CatalogValue<number>
  taxonomy_version: string
}

export interface HomeResponse {
  active_journals: AnalysisCollection<JournalActivityItem>
  catalog_generation: string
  citation_momentum: AnalysisCollection<CitationMomentumItem>
  coverage: HomeCoverage
  entity_momentum: AnalysisCollection<EntityMomentumItem>
  evidence_gaps: string[]
  generated_at: string
  latest_papers: AnalysisCollection<PaperSummary>
  research_opportunities: AnalysisCollection<ResearchOpportunity>
  scope: {
    jcr_metric_year: number
    taxonomy_version: string
  }
  subject_momentum: AnalysisCollection<SubjectTrendEstimate>
}

export interface DistributionBucket {
  count: CatalogValue<number>
  label: string
  ratio: CatalogValue<number>
}

export interface SubjectSummary extends TaxonomyReference {
  description: CatalogValue<string>
  jcr_metric_year: number
  journal_count: CatalogValue<number>
  paper_count: CatalogValue<number>
  taxonomy_version: string
}

export interface SubjectListResponse {
  analysis: AnalysisMetadata
  catalog_generation: string
  items: SubjectSummary[]
  jcr_metric_year: number
  pagination: Pagination
  taxonomy_version: string
}

export interface SubjectDetailResponse extends SubjectSummary {
  active_journals: AnalysisCollection<JournalActivityItem>
  evidence_gaps: string[]
  mesh_distribution: AnalysisCollection<DistributionBucket>
  publication_type_distribution: AnalysisCollection<DistributionBucket>
  recent_papers: AnalysisCollection<PaperSummary> & {
    pagination: Pagination
  }
  trend_estimates: AnalysisCollection<SubjectTrendEstimate>
}

export interface JournalCurationEvidenceItem {
  label: string
  value: string
}

export interface JournalCuration {
  assessed_at: string
  decision: "accepted"
  evidence: JournalCurationEvidenceItem[]
  matched_rules: string[]
  metric_year: number
  policy_name: string
  policy_version: number
}

export interface EditorialPatternEstimate {
  adjusted_p_value: CatalogValue<number>
  baseline: string
  confidence_interval: ConfidenceInterval
  coverage_ratio: CatalogValue<number>
  dimension: string
  estimate: CatalogValue<number>
  estimate_kind: "odds_ratio" | "rate_ratio"
  label: string
  support_papers: CatalogValue<number>
}

export interface JournalListResponse {
  analysis: AnalysisMetadata
  catalog_generation: string
  items: JournalSummary[]
  jcr_metric_year: number
  pagination: Pagination
  taxonomy_version: string
}

export interface JournalDetailResponse extends JournalSummary {
  curation: JournalCuration
  editorial_patterns: AnalysisCollection<EditorialPatternEstimate>
  evidence_gaps: string[]
  recent_papers: AnalysisCollection<PaperSummary> & {
    pagination: Pagination
  }
  taxonomy_version: string
}
