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
    expect(wrapper.find('aside[aria-label="发现导航"]').exists()).toBe(false)
    expect(wrapper.get('a[href="/papers#papers-q"]').text()).toBe("搜索")
    expect(wrapper.get("main#main-content").attributes("tabindex")).toBe("-1")
    expect(wrapper.get("footer").exists()).toBe(true)
  })

  it("prioritizes route meta titles and labels supported dynamic paths", async () => {
    const wrapper = await mountSuspended(DefaultLayout, {
      route: "/",
      slots: {
        default: "<h1>Route label verification</h1>",
      },
    })
    const router = wrapper.vm.$router

    router.addRoute({
      component: { template: "<h1>Meta route</h1>" },
      meta: { title: "自定义研究视图" },
      path: "/custom-view",
    })
    await router.push("/custom-view")
    await nextTick()
    expect(wrapper.get(".app-header__route-name").text()).toBe("自定义研究视图")

    for (const [path, expected] of [
      ["/", "今日"],
      ["/papers/paper-1", "论文详情"],
      ["/subjects/oncology", "学科"],
      ["/journals/nature-medicine", "期刊"],
    ] as const) {
      router.addRoute({
        component: { template: `<h1>${expected}</h1>` },
        path,
      })
      await router.push(path)
      await nextTick()
      expect(wrapper.get(".app-header__route-name").text()).toBe(expected)
    }

    expect(wrapper.text()).not.toContain("当前页面")
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
