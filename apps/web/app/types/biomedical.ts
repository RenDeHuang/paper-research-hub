import type { components } from "./openapi.generated"

export type AnalysisMetadata =
  components["schemas"]["AnalysisMetadata"]

export type AnalysisCollection<T> = Omit<
  components["schemas"]["SubjectTrendCollection"],
  "items"
> & {
  items: T[]
}

export type ConfidenceInterval =
  components["schemas"]["ConfidenceInterval"]
export type SubjectTrendEstimate =
  components["schemas"]["SubjectTrendEstimate"]
export type CitationMomentumItem =
  components["schemas"]["CitationMomentumItem"]
export type JournalMetricCategory =
  components["schemas"]["JournalMetricCategory"]
export type JournalSummary = components["schemas"]["JournalSummary"]
export type JournalActivityItem =
  components["schemas"]["JournalActivityItem"]
export type BiomedicalEntityType =
  components["schemas"]["EntityMomentumItem"]["entity_type"]
export type EntityMomentumItem =
  components["schemas"]["EntityMomentumItem"]
export type HomeCoverage = components["schemas"]["HomeCoverage"]
export type HomePublicationUpdates =
  components["schemas"]["HomePublicationUpdates"]
export type HomeResponse = components["schemas"]["HomeResponse"]
export type PublicationUpdateCollection =
  components["schemas"]["PublicationUpdateCollection"]
export type PublicationUpdateEvent =
  components["schemas"]["PublicationUpdateEvent"]
export type PublicationUpdateItem =
  components["schemas"]["PublicationUpdateItem"]
export type DistributionBucket =
  components["schemas"]["DistributionBucket"]
export type SubjectSummary = components["schemas"]["SubjectSummary"]
export type SubjectListResponse =
  components["schemas"]["SubjectListResponse"]
export type SubjectDetailResponse =
  components["schemas"]["SubjectDetailResponse"]
export type JournalCurationEvidenceItem =
  components["schemas"]["JournalCurationEvidenceItem"]
export type JournalCuration = components["schemas"]["JournalCuration"]
export type EditorialPatternEstimate =
  components["schemas"]["EditorialPatternEstimate"]
export type JournalListResponse =
  components["schemas"]["JournalListResponse"]
export type JournalDetailResponse =
  components["schemas"]["JournalDetailResponse"]
