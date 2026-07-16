import { mountSuspended } from "@nuxt/test-utils/runtime"
import { describe, expect, it } from "vitest"

import IndexPage from "../app/pages/index.vue"

describe("minimal home shell", () => {
  it("renders search and an honest no-data state without fixture papers", async () => {
    const wrapper = await mountSuspended(IndexPage)

    expect(wrapper.get("h1").text()).toBe("论文研究情报，从发现到证据")
    expect(wrapper.get('form[role="search"]').exists()).toBe(true)
    expect(wrapper.get('[data-state="empty"] h2').text()).toBe("等待 API 数据")
    expect(wrapper.find(".paper-card").exists()).toBe(false)
  })
})
