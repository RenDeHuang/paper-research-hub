// @vitest-environment node

import { fileURLToPath } from "node:url"

import { fetch, setup } from "@nuxt/test-utils/e2e"
import { describe, expect, it } from "vitest"

await setup({
  rootDir: fileURLToPath(new URL("..", import.meta.url)),
  browser: false,
})

describe("/health", () => {
  it("serves the health payload over Nitro HTTP", async () => {
    const response = await fetch("/health")

    expect(response.status).toBe(200)
    expect(response.headers.get("content-type")).toBe("application/json")
    await expect(response.json()).resolves.toEqual({
      status: "ok",
      service: "medpaperhub-web",
    })
  })

  it("rejects POST without falling through to SSR HTML", async () => {
    const response = await fetch("/health", {
      method: "POST",
    })

    expect(response.status).toBe(405)
    expect(response.headers.get("content-type")).toBe(
      "application/problem+json",
    )
    expect(response.headers.get("allow")).toBe("GET")
    await expect(response.json()).resolves.toEqual({
      type: "urn:paper-hub:problem:method-not-allowed",
      title: "Method Not Allowed",
      status: 405,
      detail: "Only GET is supported for /health.",
      instance: "/health",
    })
  })
})
