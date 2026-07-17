// @vitest-environment node

import { existsSync, readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

import { describe, expect, it } from "vitest"

const webRoot = fileURLToPath(new URL("..", import.meta.url))
const requiredPages = [
  "app/pages/index.vue",
  "app/pages/papers/index.vue",
  "app/pages/papers/[id].vue",
  "app/pages/subjects/index.vue",
  "app/pages/subjects/[slug].vue",
  "app/pages/journals/index.vue",
  "app/pages/journals/[slug].vue",
  "app/pages/topics/index.vue",
  "app/pages/topics/[slug].vue",
  "app/pages/methods/index.vue",
  "app/pages/methods/[slug].vue",
  "app/pages/trends.vue",
  "app/pages/opportunities.vue",
]
const pageOperations = new Map([
  ["app/pages/index.vue", ["getHome"]],
  ["app/pages/papers/index.vue", ["listPapers"]],
  ["app/pages/papers/[id].vue", ["getPaper"]],
  ["app/pages/subjects/index.vue", ["listSubjects"]],
  ["app/pages/subjects/[slug].vue", ["getSubject"]],
  ["app/pages/journals/index.vue", ["listJournals"]],
  ["app/pages/journals/[slug].vue", ["getJournal"]],
  ["app/pages/topics/index.vue", ["listTopics"]],
  ["app/pages/topics/[slug].vue", ["getTopic", "listPapers"]],
  ["app/pages/methods/index.vue", ["listMethods"]],
  ["app/pages/methods/[slug].vue", ["getMethod", "listPapers"]],
  ["app/pages/trends.vue", ["getHome"]],
  ["app/pages/opportunities.vue", ["listResearchOpportunities"]],
])

describe("public portal page contract", () => {
  it("implements every public route as a Nuxt page", () => {
    for (const relativePath of requiredPages) {
      expect(
        existsSync(`${webRoot}/${relativePath}`),
        `${relativePath} must exist`,
      ).toBe(true)
    }
  })

  it("declares only public.apiBaseUrl in Nuxt runtimeConfig", () => {
    const configSource = readFileSync(`${webRoot}/nuxt.config.ts`, "utf8")
    const composableSource = readFileSync(
      `${webRoot}/app/composables/useCatalogApi.ts`,
      "utf8",
    )

    expect(configSource).toContain("runtimeConfig")
    expect(configSource).toContain("public")
    expect(configSource).toMatch(/\bapiBaseUrl\s*:/)
    expect(configSource).not.toMatch(/\bapiBase\s*:/)
    expect(configSource).not.toContain("127.0.0.1")
    expect(composableSource).toContain("config.public.apiBaseUrl")
    expect(composableSource).not.toContain("config.public.apiBase)")
  })

  it("selects INTERNAL_API_BASE_URL only during SSR", () => {
    const source = readFileSync(
      `${webRoot}/app/composables/useCatalogApi.ts`,
      "utf8",
    )
    const serverGuard = ["import", "meta", "server"].join(".")

    expect(source).toContain(serverGuard)
    expect(source).toContain("process.env.INTERNAL_API_BASE_URL")
    expect(source).toContain("config.public.apiBaseUrl")
    expect(source).not.toContain("127.0.0.1")
  })

  it("does not ship a static paper, trend, or opportunity payload", () => {
    for (const relativePath of requiredPages) {
      if (!existsSync(`${webRoot}/${relativePath}`)) {
        continue
      }
      const source = readFileSync(`${webRoot}/${relativePath}`, "utf8")

      expect(source, relativePath).not.toMatch(
        /\b(?:mock|fixture|samplePapers|examplePapers|fakeTrend)\b/i,
      )
      expect(source, relativePath).not.toContain("示例论文")
      expect(source, relativePath).not.toContain("伪造趋势")
    }
  })

  it("loads every route from its corresponding typed API operation", () => {
    for (const [relativePath, operations] of pageOperations) {
      expect(existsSync(`${webRoot}/${relativePath}`), relativePath).toBe(true)
      const source = readFileSync(`${webRoot}/${relativePath}`, "utf8")

      expect(source, relativePath).toContain("useAsyncData")
      expect(source, relativePath).toContain("settleCatalogRequest")
      for (const operation of operations) {
        expect(source, relativePath).toContain(operation)
      }
    }
  })

  it("exposes only the admitted biomedical journal-paper filters", () => {
    const source = readFileSync(
      `${webRoot}/app/pages/papers/index.vue`,
      "utf8",
    )
    const supported = [
      "q",
      "published_from",
      "published_to",
      "type",
      "sort",
    ]
    const removed = [
      "topic",
      "method",
      "has_code",
      "has_data",
      "has_benchmark",
      "status",
      "source",
    ]

    for (const parameter of supported) {
      expect(source).toContain(`name="${parameter}"`)
    }
    for (const parameter of removed) {
      expect(source).not.toContain(`name="${parameter}"`)
    }
    for (const paperType of ["preprint", "dataset", "benchmark"]) {
      expect(source).not.toContain(`value="${paperType}"`)
    }
    for (const sourceName of [
      "arxiv",
      "openreview",
      "s2",
      "semantic_scholar",
      "manual",
    ]) {
      expect(source).not.toContain(`value="${sourceName}"`)
    }
    expect(source).not.toContain('name="jif_min"')
    expect(source).not.toContain('name="jcr_quartile"')
  })

  it("uses the generation-bound biomedical home snapshot for the trends overview", () => {
    const source = readFileSync(
      `${webRoot}/app/pages/trends.vue`,
      "utf8",
    )

    for (const component of [
      "SubjectMomentumGrid",
      "CitationMomentumList",
      "EntityMomentumGrid",
    ]) {
      expect(source).toContain(component)
    }

    for (const legacyContract of [
      "listPaperTrends",
      "listTopicTrends",
      "listMethodTrends",
      "trendQueryFromRoute",
      'name="window_days"',
      "Topic 趋势",
      "Method 趋势",
    ]) {
      expect(source).not.toContain(legacyContract)
    }
  })
})
