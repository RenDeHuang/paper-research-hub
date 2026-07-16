import { mountSuspended } from "@nuxt/test-utils/runtime"
import { describe, expect, it } from "vitest"

import CatalogState from "../app/components/CatalogState.vue"
import {
  catalogDataValue,
  paperAuthors,
  paperTaxonomyLabels,
} from "../app/utils/catalogPresentation"

describe("catalog value presentation", () => {
  it("preserves known, unknown, and missing as three distinct states", () => {
    expect(catalogDataValue({ state: "known", value: 0 })).toEqual({
      state: "known",
      value: 0,
    })
    expect(catalogDataValue({ state: "unknown" })).toEqual({
      state: "unknown",
    })
    expect(catalogDataValue({ state: "missing" })).toEqual({
      state: "missing",
    })
    expect(catalogDataValue(undefined)).toEqual({
      label: "未覆盖",
      state: "unknown",
    })
  })

  it("reads both live publisher references and OpenAPI author names explicitly", () => {
    expect(
      paperAuthors([
        { id: "author-1", name: "Ada Lovelace" },
        { display_name: "Grace Hopper", id: "author-2" },
      ]),
    ).toEqual({
      state: "known",
      value: ["Ada Lovelace", "Grace Hopper"],
    })
    expect(paperAuthors(undefined)).toEqual({
      label: "作者字段未覆盖",
      state: "unknown",
    })
  })

  it("does not infer taxonomy labels from an absent collection", () => {
    expect(
      paperTaxonomyLabels([
        { id: "topic-1", name: "Agents", slug: "agents" },
        "safety",
      ]),
    ).toEqual(["Agents", "safety"])
    expect(paperTaxonomyLabels(undefined)).toEqual([])
  })
})

describe("CatalogState", () => {
  it("renders catalog_not_published as waiting for first sync", async () => {
    const wrapper = await mountSuspended(CatalogState, {
      props: {
        result: {
          problem: {
            code: "catalog_not_published",
            detail: "The public catalog has not been published.",
            instance: "/api/v1/papers",
            request_id: "request-503",
            status: 503,
            title: "Service Unavailable",
            type: "urn:paper-hub:problem:catalog-not-published",
          },
          state: "waiting",
        },
      },
    })

    expect(wrapper.get('[data-state="empty"] h2').text()).toBe("等待首次同步")
    expect(wrapper.text()).toContain("首次同步完成后")
    expect(wrapper.text()).not.toContain("重试")
  })

  it("provides a retry action and request ID for recoverable API failures", async () => {
    const wrapper = await mountSuspended(CatalogState, {
      props: {
        result: {
          error: new Error("request failed"),
          problem: {
            code: "internal_error",
            detail: "The server could not complete the request.",
            instance: "/api/v1/papers",
            request_id: "request-500",
            status: 500,
            title: "Internal Server Error",
            type: "urn:paper-hub:problem:internal-error",
          },
          state: "error",
        },
      },
    })

    expect(wrapper.get('[data-state="error"] h2').text()).toBe("暂时无法加载")
    expect(wrapper.text()).toContain("request-500")
    await wrapper.get("button").trigger("click")
    expect(wrapper.emitted("retry")).toHaveLength(1)
  })
})
