import { mountSuspended } from "@nuxt/test-utils/runtime"
import { nextTick } from "vue"
import { describe, expect, it } from "vitest"

import IndexPage from "../app/pages/index.vue"

describe("minimal home shell", () => {
  it("renders search and an honest no-data state without fixture papers", async () => {
    const wrapper = await mountSuspended(IndexPage)

    expect(wrapper.get("h1").text()).toBe("论文研究情报，从发现到证据")
    expect(wrapper.get('form[role="search"]').exists()).toBe(true)
    expect(wrapper.get('[data-state="empty"] h2').text()).toBe("等待首次同步")
    expect(wrapper.get(".sync-status").text()).toContain("尚未生成")
    expect(wrapper.get(".sync-status").text()).toContain("等待首次同步")
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
    expect(wrapper.get('[data-search-state="no-match"]').text()).toContain(
      "没有匹配结果",
    )
    expect(wrapper.find('[role="option"]').exists()).toBe(false)
  })
})
