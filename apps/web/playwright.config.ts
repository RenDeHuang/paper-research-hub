import { defineConfig, devices } from "@playwright/test"

const testApiBaseUrl = "http://127.0.0.1:3200"
const e2eRunID = String(process.pid)
const nuxtServers = [
  {
    port: 3100,
    scenario: "default",
  },
  {
    port: 3101,
    scenario: "empty",
  },
  {
    port: 3102,
    scenario: "error",
  },
] as const

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  use: {
    baseURL: "http://127.0.0.1:3100",
    trace: "on-first-retry",
  },
  webServer: [
    {
      command: "node e2e/mock-catalog-server.ts",
      reuseExistingServer: false,
      timeout: 120_000,
      url: `${testApiBaseUrl}/health`,
    },
    ...nuxtServers.map(({ port, scenario }) => {
      const scopedApiBaseUrl = `${testApiBaseUrl}/${scenario}`

      return {
        command:
          `pnpm exec nuxt dev e2e/nuxt --host 127.0.0.1 --port ${port}`,
        env: {
          E2E_NUXT_RUN_ID: e2eRunID,
          E2E_NUXT_SCENARIO: scenario,
          INTERNAL_API_BASE_URL: scopedApiBaseUrl,
          NUXT_PUBLIC_API_BASE_URL: scopedApiBaseUrl,
        },
        reuseExistingServer: false,
        timeout: 180_000,
        url: `http://127.0.0.1:${port}`,
      }
    }),
    {
      command: "node e2e/warm-nuxt-servers.ts",
      reuseExistingServer: false,
      timeout: 240_000,
      url: "http://127.0.0.1:3201/health",
    },
  ],
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
})
