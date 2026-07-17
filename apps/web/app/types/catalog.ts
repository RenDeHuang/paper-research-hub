import type {
  components,
  operations,
} from "./openapi.generated"

export type UUID = string

type KnownCatalogValue<T> = Omit<
  Extract<
    components["schemas"]["StringCatalogValue"],
    { state: "known" }
  >,
  "value"
> & {
  value: T
}

/**
 * UI normalization input accepted by presentation helpers.
 * API DTOs below reference generated component schemas directly.
 */
export type CatalogValue<T> =
  | T
  | KnownCatalogValue<T>
  | components["schemas"]["MissingCatalogValue"]
  | components["schemas"]["UnknownCatalogValue"]

export type PaperType = components["schemas"]["PaperType"]
export type PaperLifecycleStatus = components["schemas"]["PaperStatus"]
export type SourceName = components["schemas"]["SourceName"]
export type PaperSort = components["parameters"]["PaperSort"]
export type ResearchOpportunityStatus =
  components["schemas"]["ResearchOpportunityStatus"]

export type ProblemDetails = components["schemas"]["ProblemDetails"]
export type Pagination = components["schemas"]["Pagination"]
export type FacetBucket = components["schemas"]["FacetBucket"]
export type Facets = components["schemas"]["Facets"]
export type TaxonomyReference =
  components["schemas"]["TaxonomyReference"]
export type JournalReference =
  components["schemas"]["JournalReference"]
export type TaxonomyItem = components["schemas"]["TaxonomyItem"]
export type AuthorReference =
  components["schemas"]["AuthorReference"] & {
    display_name?: string
  }
export type SourceProvenance =
  components["schemas"]["SourceProvenance"]
export type CurationPayload = components["schemas"]["CurationPayload"]
export type MissingSignal = components["schemas"]["MissingSignal"]
export type RankingMetadata = components["schemas"]["RankingMetadata"]

export type PaperSummary = components["schemas"]["PaperSummary"]
export type PaperDetail = components["schemas"]["PaperDetail"]
export type StatsResponse = components["schemas"]["StatsResponse"]
export type PaperListResponse =
  components["schemas"]["PaperListResponse"]
export type TopicListResponse =
  components["schemas"]["TopicListResponse"]
export type MethodListResponse =
  components["schemas"]["MethodListResponse"]
export type TaxonomyListResponse =
  | TopicListResponse
  | MethodListResponse
export type TrendItem = components["schemas"]["TrendItem"]
export type TrendListResponse = components["schemas"]["TrendListResponse"]
export type ResearchOpportunity =
  components["schemas"]["ResearchOpportunity"]
export type ResearchOpportunityListResponse =
  components["schemas"]["ResearchOpportunityListResponse"]

type OperationQuery<Name extends keyof operations> = NonNullable<
  operations[Name]["parameters"]["query"]
>

export type PaperListQuery = OperationQuery<"listPapers">
export type PageQuery = OperationQuery<"listTopics">
export type TrendQuery = OperationQuery<"listPaperTrends">
export type OpportunityQuery =
  OperationQuery<"listResearchOpportunities">

export type CatalogQuery = Record<string, boolean | number | string>
