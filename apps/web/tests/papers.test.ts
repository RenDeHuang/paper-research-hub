import type { Component } from "vue"

import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { clearNuxtData } from "#imports"
import { beforeEach, describe, expect, it, vi } from "vitest"

const catalogApi = vi.hoisted(() => ({
  listPapers: vi.fn(),
}))

mockNuxtImport("useCatalogApi", () => () => catalogApi)

const paperPages = import.meta.glob<{ default: Component }>(
  "../app/pages/papers/*.vue",
)
const emptyPaperPage = {
  facets: {
    methods: [],
    paper_types: [],
    sources: [],
    statuses: [],
    topics: [],
  },
  items: [],
  pagination: {
    has_more: false,
    limit: 20,
    next_cursor: null,
  },
}

async function loadPaperListPage() {
  const path = "../app/pages/papers/index.vue"
  const loader = paperPages[path]

  expect(loader, `${path} must exist`).toBeDefined()
  return (await loader!()).default
}

describe("biomedical paper catalog", () => {
  beforeEach(async () => {
    await clearNuxtData("papers-catalog")
    catalogApi.listPapers.mockReset()
    catalogApi.listPapers.mockResolvedValue(emptyPaperPage)
  })

  it("keeps only admitted journal-paper controls and queries active records", async () => {
    const PaperListPage = await loadPaperListPage()
    const wrapper = await mountSuspended(PaperListPage, {
      route:
        "/papers?q=oncology&type=review&sort=relevance"
        + "&published_from=2026-07-01T00%3A00%3A00Z"
        + "&published_to=2026-07-16T23%3A59%3A59Z",
    })

    expect(catalogApi.listPapers).toHaveBeenCalledWith({
      published_from: "2026-07-01T00:00:00Z",
      published_to: "2026-07-16T23:59:59Z",
      q: "oncology",
      sort: "relevance",
      status: "active",
      type: "review",
    })
    expect(
      wrapper.get<HTMLSelectElement>('[name="type"]').findAll("option")
        .map((option) => option.attributes("value")),
    ).toEqual(["", "research_article", "review"])
    for (const removed of [
      "topic",
      "method",
      "has_code",
      "has_data",
      "has_benchmark",
      "status",
      "source",
    ]) {
      expect(wrapper.find(`[name="${removed}"]`).exists()).toBe(false)
    }
  })
})
