import {
  mockNuxtImport,
  mountSuspended,
} from "@nuxt/test-utils/runtime"
import { nextTick } from "vue"
import { beforeEach, describe, expect, it, vi } from "vitest"

import MethodDetailPage from "../app/pages/methods/[slug].vue"
import TopicDetailPage from "../app/pages/topics/[slug].vue"

const catalogApi = vi.hoisted(() => ({
  getMethod: vi.fn(),
  getTopic: vi.fn(),
  listPapers: vi.fn(),
}))

mockNuxtImport("useCatalogApi", () => () => catalogApi)

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
    total: 0,
  },
}

describe("taxonomy detail paper pagination", () => {
  beforeEach(() => {
    catalogApi.getMethod.mockReset()
    catalogApi.getTopic.mockReset()
    catalogApi.listPapers.mockReset()
    catalogApi.listPapers.mockResolvedValue(emptyPaperPage)
  })

  it("passes the Topic cursor and reloads papers when it changes", async () => {
    catalogApi.getTopic
      .mockResolvedValueOnce({
        description: { state: "unknown" },
        id: "topic-1",
        name: "Agents",
        paper_count: 0,
        slug: "agents",
      })
      .mockResolvedValueOnce({
        description: { state: "missing" },
        id: "topic-1",
        name: "Agents",
        paper_count: 0,
        slug: "agents",
      })

    const wrapper = await mountSuspended(TopicDetailPage, {
      route: "/topics/agents?cursor=topic-page-2",
    })

    try {
      expect(catalogApi.listPapers).toHaveBeenLastCalledWith({
        cursor: "topic-page-2",
        limit: 20,
        topic: "agents",
      })
      expect(wrapper.get(".page-lede").attributes("data-value-state")).toBe(
        "unknown",
      )

      await wrapper.vm.$router.push({
        path: "/topics/agents",
        query: { cursor: "topic-page-3" },
      })
      await nextTick()

      await vi.waitFor(() => {
        expect(catalogApi.listPapers).toHaveBeenLastCalledWith({
          cursor: "topic-page-3",
          limit: 20,
          topic: "agents",
        })
      })
      expect(catalogApi.listPapers).toHaveBeenCalledTimes(2)
      await vi.waitFor(() => {
        expect(wrapper.get(".page-lede").attributes("data-value-state")).toBe(
          "missing",
        )
      })
    } finally {
      wrapper.unmount()
    }
  })

  it("passes the Method cursor and reloads papers when it changes", async () => {
    catalogApi.getMethod.mockResolvedValue({
      description: { state: "known", value: "Retrieval augmented generation" },
      id: "method-1",
      name: "RAG",
      paper_count: 0,
      slug: "rag",
    })

    const wrapper = await mountSuspended(MethodDetailPage, {
      route: "/methods/rag?cursor=method-page-2",
    })

    try {
      expect(catalogApi.listPapers).toHaveBeenLastCalledWith({
        cursor: "method-page-2",
        limit: 20,
        method: "rag",
      })
      expect(wrapper.get(".page-lede").attributes("data-value-state")).toBe(
        "known",
      )

      await wrapper.vm.$router.push({
        path: "/methods/rag",
        query: { cursor: "method-page-3" },
      })
      await nextTick()

      await vi.waitFor(() => {
        expect(catalogApi.listPapers).toHaveBeenLastCalledWith({
          cursor: "method-page-3",
          limit: 20,
          method: "rag",
        })
      })
      expect(catalogApi.listPapers).toHaveBeenCalledTimes(2)
    } finally {
      wrapper.unmount()
    }
  })
})
