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
  it("maps every supported paper filter to the exact Go API query name", () => {
    expect(
      paperListQueryFromRoute({
        cursor: "opaque-cursor",
        has_benchmark: "false",
        has_code: "true",
        has_data: "false",
        limit: "40",
        method: "causal-inference",
        published_from: "2026-07-01T00:00:00Z",
        published_to: "2026-07-16T23:59:59Z",
        q: "agent evaluation",
        sort: "relevance",
        source: "openalex",
        status: "active",
        topic: "agents",
        type: "research_article",
      }),
    ).toEqual({
      cursor: "opaque-cursor",
      has_benchmark: false,
      has_code: true,
      has_data: false,
      limit: 40,
      method: "causal-inference",
      published_from: "2026-07-01T00:00:00Z",
      published_to: "2026-07-16T23:59:59Z",
      q: "agent evaluation",
      sort: "relevance",
      source: "openalex",
      status: "active",
      topic: "agents",
      type: "research_article",
    })
  })

  it.each([
    [{ unknown: "value" }, "unknown"],
    [{ q: ["agent", "safety"] }, "q"],
    [{ q: " agent" }, "q"],
    [{ has_code: "1" }, "has_code"],
    [{ limit: "0" }, "limit"],
    [{ sort: "relevance" }, "sort"],
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
      "getMethod",
      "getPaper",
      "getStats",
      "getTopic",
      "listMethodTrends",
      "listMethods",
      "listPaperTrends",
      "listPapers",
      "listResearchOpportunities",
      "listTopicTrends",
      "listTopics",
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
