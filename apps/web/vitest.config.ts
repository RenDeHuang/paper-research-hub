import { defineVitestConfig } from "@nuxt/test-utils/config"

const testApiBaseUrl = "http://catalog-api.test.invalid"

process.env.INTERNAL_API_BASE_URL = testApiBaseUrl
process.env.NUXT_PUBLIC_API_BASE_URL = testApiBaseUrl

export default defineVitestConfig({
  test: {
    environment: "nuxt",
    exclude: ["e2e/**", "tests/health.test.ts"],
  },
})
