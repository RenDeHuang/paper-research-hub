// @vitest-environment node

import { existsSync, readdirSync, readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

import { describe, expect, it } from "vitest"

const webRoot = fileURLToPath(new URL("..", import.meta.url))

const requiredFiles = [
  "app/assets/css/main.css",
  "app/layouts/default.vue",
  "app/components/AppHeader.vue",
  "app/components/AppFooter.vue",
  "app/components/SearchCommand.vue",
  "app/components/DiscoveryRail.vue",
  "app/components/SyncStatus.vue",
  "app/components/PaperCard.vue",
  "app/components/TrendSparkline.vue",
  "app/components/EvidenceBar.vue",
  "app/components/OpportunityMatrix.vue",
  "app/components/OpportunityPlot.vue",
  "app/components/DataState.vue",
  "app/pages/index.vue",
]

describe("Nuxt visual foundation contract", () => {
  it("provides every visual shell file inside apps/web", () => {
    for (const relativePath of requiredFiles) {
      expect(
        existsSync(`${webRoot}/${relativePath}`),
        `${relativePath} must exist`,
      ).toBe(true)
    }
  })

  it("delegates rendering through NuxtLayout and NuxtPage", () => {
    const source = readFileSync(`${webRoot}/app/app.vue`, "utf8")

    expect(source).toContain("<NuxtLayout>")
    expect(source).toContain("<NuxtPage />")
  })

  it("registers the approved global stylesheet", () => {
    const source = readFileSync(`${webRoot}/nuxt.config.ts`, "utf8")

    expect(source).toContain('css: ["~/assets/css/main.css"]')
  })

  it("locks the Nuxt compatibility baseline to the implementation date", () => {
    const source = readFileSync(`${webRoot}/nuxt.config.ts`, "utf8")

    expect(source).toContain('compatibilityDate: "2026-07-16"')
  })

  it("implements the approved tokens, focus treatment, motion preference, and shell breakpoints", () => {
    const source = readFileSync(`${webRoot}/app/assets/css/main.css`, "utf8")

    for (const declaration of [
      "--paper: #fbf8f2",
      "--navy-900: #173f5f",
      "--teal-700: #0b6862",
      "--coral-700: #a94335",
      "--color-bg: var(--paper)",
      "--color-primary: var(--navy-900)",
      "--color-link: var(--teal-700)",
      "--color-negative: var(--coral-700)",
      "--shell-max: 1360px",
    ]) {
      expect(source.toLowerCase()).toContain(declaration)
    }

    expect(source).toContain(":focus-visible")
    expect(source).toContain("outline: 3px solid var(--color-focus)")
    expect(source).not.toContain("outline: 0")
    expect(source).toContain(".menu-open body")
    expect(source).toContain("@media (prefers-reduced-motion: reduce)")

    for (const breakpoint of ["768px", "1024px", "1440px"]) {
      expect(source).toContain(`min-width: ${breakpoint}`)
    }
  })

  it("keeps raw palette tokens inside the global token layer", () => {
    const componentRoot = `${webRoot}/app/components`
    const componentFiles = readdirSync(componentRoot).filter((file) =>
      file.endsWith(".vue"),
    )
    const rawPaletteToken =
      /var\(--(?:border-strong|coral|ink|navy|ochre|paper|surface|teal|warm)-?\d*/

    for (const file of componentFiles) {
      const source = readFileSync(`${componentRoot}/${file}`, "utf8")
      expect(source, `${file} must only consume semantic tokens`).not.toMatch(
        rawPaletteToken,
      )
    }

    const globalSource = readFileSync(
      `${webRoot}/app/assets/css/main.css`,
      "utf8",
    )
    const globalUtilities = globalSource.slice(
      globalSource.indexOf("*,\n*::before"),
    )

    expect(globalUtilities).not.toMatch(rawPaletteToken)
  })

  it("keeps discovery rail visibility under the shell breakpoint contract", () => {
    const componentSource = readFileSync(
      `${webRoot}/app/components/DiscoveryRail.vue`,
      "utf8",
    )
    const stylesheet = readFileSync(
      `${webRoot}/app/assets/css/main.css`,
      "utf8",
    )

    expect(componentSource).not.toMatch(
      /\.discovery-rail\s*\{[^}]*display:\s*grid/s,
    )
    expect(stylesheet).toContain(
      ".app-shell__content > .discovery-rail {\n  display: none;",
    )
    expect(stylesheet).toMatch(
      /@media \(min-width: 1024px\)[\s\S]*\.app-shell__content > \.discovery-rail\s*\{[\s\S]*display:\s*grid;/,
    )
  })

  it("uses NuxtLink for every primary navigation destination", () => {
    const source = readFileSync(
      `${webRoot}/app/components/AppHeader.vue`,
      "utf8",
    )

    expect(source).toMatch(/<NuxtLink\s+:to="item\.to"/)
    expect(source).not.toMatch(/<a\s+:href="item\.to"/)
  })

  it("locks the public shell brand and biomedical first-level navigation", () => {
    const header = readFileSync(
      `${webRoot}/app/components/AppHeader.vue`,
      "utf8",
    )
    const footer = readFileSync(
      `${webRoot}/app/components/AppFooter.vue`,
      "utf8",
    )
    const config = readFileSync(`${webRoot}/nuxt.config.ts`, "utf8")

    expect(header).toContain("medpaperhub")
    expect(footer).toContain("medpaperhub")
    expect(config).toContain("medpaperhub")
    expect(header).not.toContain("Paper Research Hub")
    expect(footer).not.toContain("Paper Research Hub")
    for (const destination of [
      'label: "首页"',
      'label: "学科"',
      'label: "期刊"',
      'label: "论文"',
      'label: "趋势"',
      'label: "研究机会"',
    ]) {
      expect(header).toContain(destination)
    }
  })
})
