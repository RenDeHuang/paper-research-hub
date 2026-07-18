import { tmpdir } from "node:os"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import { defineNuxtConfig } from "nuxt/config"

const appRoot = fileURLToPath(new URL("../..", import.meta.url))
const baseNuxtConfigPath = fileURLToPath(
  new URL("../../nuxt.config.ts", import.meta.url),
)
const { default: baseNuxtConfig } = await import(baseNuxtConfigPath) as {
  default: ReturnType<typeof defineNuxtConfig>
}
const runID = process.env.E2E_NUXT_RUN_ID
const scenario = process.env.E2E_NUXT_SCENARIO

if (runID === undefined || !/^[A-Za-z0-9_-]+$/.test(runID)) {
  throw new Error("E2E_NUXT_RUN_ID must contain only letters, digits, _ or -")
}
if (!["default", "empty", "error"].includes(scenario ?? "")) {
  throw new Error("E2E_NUXT_SCENARIO must be default, empty or error")
}

export default defineNuxtConfig({
  ...baseNuxtConfig,
  buildDir: join(
    tmpdir(),
    "medpaperhub-playwright",
    runID,
    scenario!,
    ".nuxt",
  ),
  rootDir: appRoot,
})
