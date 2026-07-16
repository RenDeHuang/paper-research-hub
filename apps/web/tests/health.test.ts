// @vitest-environment node

import { existsSync } from "node:fs"

import { describe, expect, it } from "vitest"

const healthRouteURL = new URL("../server/routes/health.get.ts", import.meta.url)

describe("GET /health", () => {
  it("returns the web service health payload", async () => {
    expect(existsSync(healthRouteURL)).toBe(true)

    const healthRoute = await import(
      /* @vite-ignore */ healthRouteURL.href
    )

    expect(await healthRoute.default()).toEqual({
      status: "ok",
      service: "paper-hub-web",
    })
  })
})
