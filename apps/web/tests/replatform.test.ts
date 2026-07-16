import { mountSuspended } from "@nuxt/test-utils/runtime"
import { nextTick } from "vue"
import { describe, expect, it, vi } from "vitest"

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
    expect(wrapper.get('aside[aria-label="发现导航"]').exists()).toBe(true)
    expect(wrapper.get("main#main-content").attributes("tabindex")).toBe("-1")
    expect(wrapper.get("footer").exists()).toBe(true)
  })

  it("moves focus only when the pathname changes", async () => {
    const wrapper = await mountSuspended(DefaultLayout, {
      route: "/",
      slots: {
        default: '<h1 id="global-search">Shell verification</h1>',
      },
    })
    const router = wrapper.vm.$router
    const focus = vi.spyOn(
      wrapper.get("main#main-content").element as HTMLElement,
      "focus",
    )

    await nextTick()
    focus.mockClear()
    await router.push({ path: "/", query: { q: "agent" } })
    await nextTick()
    expect(focus).not.toHaveBeenCalled()

    await router.push({ hash: "#global-search", path: "/" })
    await nextTick()
    expect(focus).not.toHaveBeenCalled()

    router.addRoute({
      component: { template: "<h1>Focus target</h1>" },
      path: "/focus-target",
    })
    await router.push("/focus-target")
    await nextTick()

    expect(focus).toHaveBeenCalledTimes(1)
    expect(focus).toHaveBeenCalledWith({ preventScroll: true })
    focus.mockRestore()
  })
})
