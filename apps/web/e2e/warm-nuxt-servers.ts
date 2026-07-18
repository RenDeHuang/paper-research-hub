import { createServer } from "node:http"

import { chromium } from "@playwright/test"

const host = "127.0.0.1"
const port = 3201
const targets = [
  "http://127.0.0.1:3100",
  "http://127.0.0.1:3101",
  "http://127.0.0.1:3102",
] as const

const browser = await chromium.launch()

try {
  for (const target of targets) {
    const page = await browser.newPage()
    const runtimeErrors: string[] = []

    page.on("console", (message) => {
      if (message.type() === "error" || /hydration/i.test(message.text())) {
        runtimeErrors.push(message.text())
      }
    })
    page.on("pageerror", (error) => {
      runtimeErrors.push(error.message)
    })

    const response = await page.goto(target, {
      timeout: 120_000,
      waitUntil: "load",
    })
    if (response === null || response.status() !== 200) {
      throw new Error(
        `Nuxt prewarm failed for ${target}: document status ${
          response?.status() ?? "missing"
        }`,
      )
    }

    await page.waitForFunction(
      () => {
        const menuButton = document.querySelector<HTMLButtonElement>(
          ".app-header__menu-button",
        )
        return menuButton?.disabled === false
      },
      undefined,
      { timeout: 120_000 },
    )

    if (runtimeErrors.length > 0) {
      throw new Error(
        `Nuxt prewarm failed for ${target}: ${runtimeErrors.join(" | ")}`,
      )
    }
    await page.close()
  }
} finally {
  await browser.close()
}

const server = createServer((request, response) => {
  if (request.url === "/health") {
    response.writeHead(200, {
      "Content-Type": "application/json; charset=utf-8",
    })
    response.end(JSON.stringify({
      status: "ready",
      targets,
    }))
    return
  }

  response.writeHead(404)
  response.end()
})

server.listen(port, host, () => {
  process.stdout.write(
    `Nuxt E2E prewarm complete at http://${host}:${port}/health\n`,
  )
})

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    server.close(() => process.exit(0))
  })
}
