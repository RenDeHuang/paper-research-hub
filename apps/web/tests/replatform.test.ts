import { mountSuspended } from "@nuxt/test-utils/runtime"
import { describe, expect, it } from "vitest"

import App from "../app/app.vue"

describe("replatform baseline", () => {
  it("associates the main landmark with its unique status heading", async () => {
    const wrapper = await mountSuspended(App)
    const main = wrapper.get("main")

    expect(main.attributes("aria-labelledby")).toBe("replatform-heading")
    expect(wrapper.findAll("#replatform-heading")).toHaveLength(1)
    expect(wrapper.get("h1#replatform-heading").text()).toBe("Go + Nuxt 重构中")
  })
})
