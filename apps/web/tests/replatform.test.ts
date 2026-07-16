import { mountSuspended } from "@nuxt/test-utils/runtime"
import { describe, expect, it } from "vitest"

import DefaultLayout from "../app/layouts/default.vue"

describe("default application shell", () => {
  it("provides skip navigation and the expected landmarks", async () => {
    const wrapper = await mountSuspended(DefaultLayout, {
      route: "/",
      slots: {
        default: "<h1>Shell verification</h1>",
      },
    })

    expect(wrapper.get('a[href="#main-content"]').text()).toBe("跳到主要内容")
    expect(wrapper.get("header").exists()).toBe(true)
    expect(wrapper.get('nav[aria-label="一级导航"]').exists()).toBe(true)
    expect(wrapper.get("main#main-content").attributes("tabindex")).toBe("-1")
    expect(wrapper.get("footer").exists()).toBe(true)
  })
})
