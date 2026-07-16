import { mountSuspended } from "@nuxt/test-utils/runtime"
import { nextTick } from "vue"
import { describe, expect, it } from "vitest"

import IndexPage from "../app/pages/index.vue"

describe("minimal home shell", () => {
  it("renders search and an explicit recovery path without fixture papers", async () => {
    const wrapper = await mountSuspended(IndexPage)

    expect(wrapper.get("h1").text()).toBe("论文研究情报，从发现到证据")
    expect(wrapper.get('form[role="search"]').exists()).toBe(true)
    expect(wrapper.get('[data-state="error"] h2').text()).toBe("暂时无法加载")
    expect(wrapper.get('[data-state="error"] button').text()).toBe("重试")
    expect(wrapper.get(".sync-status").text()).toContain("尚未加载")
    expect(wrapper.get(".sync-status").text()).toContain("API 未提供时间范围")
    expect(wrapper.text()).not.toMatch(/\d{4}-\d{2}-\d{2}/)
    expect(wrapper.text()).not.toContain("0%")
    expect(wrapper.find(".paper-card").exists()).toBe(false)
  })

  it("controls the empty suggestion search without inventing results", async () => {
    const wrapper = await mountSuspended(IndexPage)
    const input = wrapper.get('[role="combobox"]')

    await input.setValue("agent")
    await nextTick()

    expect(input.element.value).toBe("agent")
    expect(wrapper.get('[data-search-state="unavailable"]').text()).toContain(
      "当前 API 未提供搜索建议端点",
    )
    expect(wrapper.find('[role="option"]').exists()).toBe(false)
  })
})
