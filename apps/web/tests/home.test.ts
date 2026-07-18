import { mountSuspended } from "@nuxt/test-utils/runtime"
import { describe, expect, it } from "vitest"

import IndexPage from "../app/pages/index.vue"

describe("minimal home shell", () => {
  it("renders an explicit recovery path without a hero or invented papers", async () => {
    const wrapper = await mountSuspended(IndexPage)

    expect(wrapper.get("h1").text()).toBe("medpaperhub 今日论文情报")
    expect(wrapper.get("h1").classes()).toContain("visually-hidden")
    expect(wrapper.find('form[role="search"]').exists()).toBe(false)
    expect(wrapper.get('[data-state="error"] h2').text()).toBe("暂时无法加载")
    expect(wrapper.get('[data-state="error"] button').text()).toBe("重试")
    expect(wrapper.find(".sync-status").exists()).toBe(false)
    expect(wrapper.text()).not.toContain(
      "医学生物学研究情报，从新论文到可验证趋势",
    )
    expect(wrapper.text()).not.toContain("查看重点学科和期刊")
    expect(wrapper.text()).not.toMatch(/\d{4}-\d{2}-\d{2}/)
    expect(wrapper.find("[data-event-kind]").exists()).toBe(false)
  })
})
