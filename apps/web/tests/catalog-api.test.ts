import { describe, expect, it } from "vitest"

import {
  CatalogApiError,
  createCatalogApiClient,
  resolveCatalogApiBase,
} from "../app/utils/catalogApi"
import {
  paperListQueryFromRoute,
  CatalogQueryError,
  pageQueryFromRoute,
  trendQueryFromRoute,
} from "../app/utils/catalogQuery"
import { settleCatalogRequest } from "../app/utils/catalogResult"

describe("catalog query contract", () => {
  it("maps the biomedical paper catalog query and fixes lifecycle to active", () => {
    expect(
      paperListQueryFromRoute({
        cursor: "opaque-cursor",
        limit: "40",
        published_from: "2026-07-01T00:00:00Z",
        published_to: "2026-07-16T23:59:59Z",
        q: "oncology cohort",
        sort: "relevance",
        type: "research_article",
      }),
    ).toEqual({
      cursor: "opaque-cursor",
      limit: 40,
      published_from: "2026-07-01T00:00:00Z",
      published_to: "2026-07-16T23:59:59Z",
      q: "oncology cohort",
      sort: "relevance",
      status: "active",
      type: "research_article",
    })
  })

  it.each([
    [{ unknown: "value" }, "unknown"],
    [{ q: ["agent", "safety"] }, "q"],
    [{ q: " agent" }, "q"],
    [{ limit: "0" }, "limit"],
    [{ sort: "relevance" }, "sort"],
    [{ type: "preprint" }, "type"],
    [{ type: "dataset" }, "type"],
    [{ type: "benchmark" }, "type"],
    [{ topic: "oncology" }, "topic"],
    [{ method: "causal-inference" }, "method"],
    [{ has_code: "true" }, "has_code"],
    [{ has_data: "true" }, "has_data"],
    [{ has_benchmark: "true" }, "has_benchmark"],
    [{ status: "retracted" }, "status"],
    [{ source: "arxiv" }, "source"],
    [
      {
        published_from: "2026-07-17T00:00:00Z",
        published_to: "2026-07-16T00:00:00Z",
      },
      "published_from",
    ],
  ])("rejects invalid route query %j at %s", (query, parameter) => {
    expect(() => paperListQueryFromRoute(query)).toThrow(parameter)
  })

  it("keeps cursor pagination bound to the supported page contract", () => {
    expect(pageQueryFromRoute({ cursor: "next-page", limit: "20" })).toEqual({
      cursor: "next-page",
      limit: 20,
    })
    expect(() => pageQueryFromRoute({ q: "not-supported" })).toThrow("q")
  })

  it("maps the trend window and rejects repeated values", () => {
    expect(
      trendQueryFromRoute({
        cursor: "next-window",
        limit: "10",
        window_days: "90",
      }),
    ).toEqual({
      cursor: "next-window",
      limit: 10,
      window_days: 90,
    })
    expect(() =>
      trendQueryFromRoute({ window_days: ["30", "90"] }),
    ).toThrow("window_days")
  })
})

describe("catalog API client contract", () => {
  it("uses the internal base for SSR and the public base for browsers", () => {
    expect(
      resolveCatalogApiBase({
        internalApiBaseUrl: "http://api.internal:8080/",
        publicApiBaseUrl: "https://catalog.example.test/api/",
        server: true,
      }),
    ).toBe("http://api.internal:8080")
    expect(
      resolveCatalogApiBase({
        internalApiBaseUrl: "http://api.internal:8080/",
        publicApiBaseUrl: "https://catalog.example.test/api/",
        server: false,
      }),
    ).toBe("https://catalog.example.test/api")
  })

  it("rejects missing SSR configuration instead of falling back to the public base", () => {
    expect(() =>
      resolveCatalogApiBase({
        internalApiBaseUrl: undefined,
        publicApiBaseUrl: "https://catalog.example.test",
        server: true,
      }),
    ).toThrow("INTERNAL_API_BASE_URL")
  })

  it("rejects missing browser configuration instead of falling back to the internal base", () => {
    expect(() =>
      resolveCatalogApiBase({
        internalApiBaseUrl: "http://api.internal:8080",
        publicApiBaseUrl: "",
        server: false,
      }),
    ).toThrow("runtimeConfig.public.apiBaseUrl")
  })

  it("exposes every public catalog operation against one normalized API base", () => {
    const client = createCatalogApiClient("http://api.internal:8080/")

    expect(client.baseURL).toBe("http://api.internal:8080")
    expect(Object.keys(client).sort()).toEqual([
      "baseURL",
      "getHome",
      "getJournal",
      "getMethod",
      "getPaper",
      "getStats",
      "getSubject",
      "getTopic",
      "listJournals",
      "listMethodTrends",
      "listMethods",
      "listPaperTrends",
      "listPapers",
      "listResearchOpportunities",
      "listSubjects",
      "listTopicTrends",
      "listTopics",
    ])
  })

  it("uses only the generation-bound biomedical discovery endpoints", async () => {
    const calls: Array<{
      options: {
        baseURL: string
        query?: Record<string, boolean | number | string>
      }
      path: string
    }> = []
    const client = createCatalogApiClient(
      "http://api.internal:8080",
      async (path, options) => {
        calls.push({ options, path })
        return {} as never
      },
    )

    await client.getHome()
    await client.listSubjects({ cursor: "subject-page-2" })
    await client.getSubject("oncology", { cursor: "subject-paper-page-2" })
    await client.listJournals({ cursor: "journal-page-2" })
    await client.getJournal(
      "journal-of-clinical-oncology",
      { cursor: "journal-paper-page-2" },
    )

    expect(calls).toEqual([
      {
        options: { baseURL: "http://api.internal:8080" },
        path: "/api/v1/home",
      },
      {
        options: {
          baseURL: "http://api.internal:8080",
          query: { cursor: "subject-page-2" },
        },
        path: "/api/v1/subjects",
      },
      {
        options: {
          baseURL: "http://api.internal:8080",
          query: { cursor: "subject-paper-page-2" },
        },
        path: "/api/v1/subjects/oncology",
      },
      {
        options: {
          baseURL: "http://api.internal:8080",
          query: { cursor: "journal-page-2" },
        },
        path: "/api/v1/journals",
      },
      {
        options: {
          baseURL: "http://api.internal:8080",
          query: { cursor: "journal-paper-page-2" },
        },
        path: "/api/v1/journals/journal-of-clinical-oncology",
      },
    ])
  })

  it("classifies catalog_not_published as the first-sync waiting state", async () => {
    const problem = {
      code: "catalog_not_published",
      detail: "The public catalog has not been published.",
      instance: "/api/v1/papers",
      request_id: "request-503",
      status: 503,
      title: "Service Unavailable",
      type: "urn:paper-hub:problem:catalog-not-published",
    } as const

    const result = await settleCatalogRequest(
      Promise.reject(new CatalogApiError(problem)),
    )

    expect(result).toEqual({
      problem,
      state: "waiting",
    })
  })

  it("keeps validation, not-found, and retryable failures distinct", async () => {
    const cases = [
      ["validation_error", 422, "invalid"],
      ["resource_not_found", 404, "not-found"],
      ["internal_error", 500, "error"],
    ] as const

    for (const [code, status, expectedState] of cases) {
      const problem = {
        code,
        detail: "Contract failure",
        instance: "/api/v1/resource",
        request_id: `request-${status}`,
        status,
        title: "Request failed",
        type: `urn:paper-hub:problem:${code}`,
      }
      const result = await settleCatalogRequest(
        Promise.reject(new CatalogApiError(problem)),
      )

      expect(result.state).toBe(expectedState)
    }
  })

  it("classifies a synchronous route query failure before any API call", async () => {
    const result = await settleCatalogRequest(() => {
      throw new CatalogQueryError("q")
    })

    expect(result.state).toBe("invalid")
  })

  it("returns a serializable plain error for Nuxt SSR payloads", async () => {
    const result = await settleCatalogRequest(
      Promise.reject(new Error("network unavailable")),
    )

    expect(result).toEqual({
      error: {
        message: "network unavailable",
        name: "Error",
      },
      state: "error",
    })
    if (result.state === "error") {
      expect(Object.getPrototypeOf(result.error)).toBe(Object.prototype)
    }
  })
})
