import type {
  CatalogQuery,
  OpportunityQuery,
  PageQuery,
  PaperDetail,
  PaperListQuery,
  PaperListResponse,
  ProblemDetails,
  ResearchOpportunityListResponse,
  StatsResponse,
  TaxonomyItem,
  TaxonomyListResponse,
  TrendListResponse,
  TrendQuery,
} from "~/types/catalog"
import type {
  HomeResponse,
  JournalDetailResponse,
  JournalListResponse,
  SubjectDetailResponse,
  SubjectListResponse,
} from "~/types/biomedical"

interface FetchOptions {
  baseURL: string
  query?: CatalogQuery
}

interface CatalogFetcher {
  <T>(request: string, options: FetchOptions): Promise<T>
}

export class CatalogApiError extends Error {
  readonly problem: ProblemDetails

  constructor(problem: ProblemDetails) {
    super(problem.detail)
    this.name = "CatalogApiError"
    this.problem = problem
  }
}

export interface CatalogApiClient {
  readonly baseURL: string
  getHome(): Promise<HomeResponse>
  getJournal(
    slug: string,
    query?: PageQuery,
  ): Promise<JournalDetailResponse>
  getStats(): Promise<StatsResponse>
  getSubject(
    slug: string,
    query?: PageQuery,
  ): Promise<SubjectDetailResponse>
  listPapers(query?: PaperListQuery): Promise<PaperListResponse>
  getPaper(id: string): Promise<PaperDetail>
  listJournals(query?: PageQuery): Promise<JournalListResponse>
  listSubjects(query?: PageQuery): Promise<SubjectListResponse>
  listTopics(query?: PageQuery): Promise<TaxonomyListResponse>
  getTopic(slug: string): Promise<TaxonomyItem>
  listMethods(query?: PageQuery): Promise<TaxonomyListResponse>
  getMethod(slug: string): Promise<TaxonomyItem>
  listPaperTrends(query?: TrendQuery): Promise<TrendListResponse>
  listTopicTrends(query?: TrendQuery): Promise<TrendListResponse>
  listMethodTrends(query?: TrendQuery): Promise<TrendListResponse>
  listResearchOpportunities(
    query?: OpportunityQuery,
  ): Promise<ResearchOpportunityListResponse>
}

export interface CatalogApiBaseOptions {
  readonly internalApiBaseUrl: string | undefined
  readonly publicApiBaseUrl: string | undefined
  readonly server: boolean
}

export function resolveCatalogApiBase(options: CatalogApiBaseOptions): string {
  const source = options.server
    ? "INTERNAL_API_BASE_URL"
    : "runtimeConfig.public.apiBaseUrl"
  const apiBaseUrl = options.server
    ? options.internalApiBaseUrl
    : options.publicApiBaseUrl

  return normalizeApiBase(apiBaseUrl, source)
}

export function createCatalogApiClient(
  apiBase: string,
  suppliedFetcher?: CatalogFetcher,
): CatalogApiClient {
  const baseURL = normalizeApiBase(apiBase, "catalog API base URL")
  const fetcher: CatalogFetcher =
    suppliedFetcher
    ?? (async <T>(request: string, options: FetchOptions) =>
      (await $fetch<unknown>(request, options)) as T)

  async function request<T>(path: string, query?: CatalogQuery) {
    try {
      return await fetcher<T>(path, {
        baseURL,
        ...(query === undefined ? {} : { query }),
      })
    } catch (error: unknown) {
      const problem = extractProblem(error)
      if (problem !== undefined) {
        throw new CatalogApiError(problem)
      }
      throw error
    }
  }

  return {
    baseURL,
    getHome: () => request<HomeResponse>("/api/v1/home"),
    getJournal: (slug, query = {}) =>
      request<JournalDetailResponse>(
        `/api/v1/journals/${encodeURIComponent(slug)}`,
        query as CatalogQuery,
      ),
    getMethod: (slug) =>
      request<TaxonomyItem>(
        `/api/v1/methods/${encodeURIComponent(slug)}`,
      ),
    getPaper: (id) =>
      request<PaperDetail>(`/api/v1/papers/${encodeURIComponent(id)}`),
    getStats: () => request<StatsResponse>("/api/v1/stats"),
    getSubject: (slug, query = {}) =>
      request<SubjectDetailResponse>(
        `/api/v1/subjects/${encodeURIComponent(slug)}`,
        query as CatalogQuery,
      ),
    getTopic: (slug) =>
      request<TaxonomyItem>(
        `/api/v1/topics/${encodeURIComponent(slug)}`,
      ),
    listMethodTrends: (query = {}) =>
      request<TrendListResponse>(
        "/api/v1/trends/methods",
        query as CatalogQuery,
      ),
    listMethods: (query = {}) =>
      request<TaxonomyListResponse>(
        "/api/v1/methods",
        query as CatalogQuery,
      ),
    listJournals: (query = {}) =>
      request<JournalListResponse>(
        "/api/v1/journals",
        query as CatalogQuery,
      ),
    listPaperTrends: (query = {}) =>
      request<TrendListResponse>(
        "/api/v1/trends/papers",
        query as CatalogQuery,
      ),
    listPapers: (query = {}) =>
      request<PaperListResponse>(
        "/api/v1/papers",
        query as CatalogQuery,
      ),
    listResearchOpportunities: (query = {}) =>
      request<ResearchOpportunityListResponse>(
        "/api/v1/research-opportunities",
        query as CatalogQuery,
      ),
    listSubjects: (query = {}) =>
      request<SubjectListResponse>(
        "/api/v1/subjects",
        query as CatalogQuery,
      ),
    listTopicTrends: (query = {}) =>
      request<TrendListResponse>(
        "/api/v1/trends/topics",
        query as CatalogQuery,
      ),
    listTopics: (query = {}) =>
      request<TaxonomyListResponse>(
        "/api/v1/topics",
        query as CatalogQuery,
      ),
  }
}

function normalizeApiBase(apiBase: string | undefined, source: string) {
  if (
    apiBase === undefined
    || apiBase.length === 0
    || apiBase !== apiBase.trim()
  ) {
    throw new Error(`${source} 必须是非空且无首尾空白的 URL`)
  }

  let url: URL
  try {
    url = new URL(apiBase)
  } catch {
    throw new Error(`${source} 必须是有效的绝对 URL`)
  }

  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error(`${source} 仅支持 http 或 https`)
  }
  return apiBase.replace(/\/+$/, "")
}

function extractProblem(error: unknown): ProblemDetails | undefined {
  if (!isRecord(error)) {
    return undefined
  }
  if (isProblemDetails(error.data)) {
    return error.data
  }
  if (isProblemDetails(error)) {
    return error
  }
  return undefined
}

function isProblemDetails(value: unknown): value is ProblemDetails {
  if (!isRecord(value)) {
    return false
  }
  return (
    typeof value.type === "string"
    && typeof value.title === "string"
    && typeof value.status === "number"
    && typeof value.code === "string"
    && typeof value.detail === "string"
    && typeof value.instance === "string"
    && typeof value.request_id === "string"
  )
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}
