import { expect, test, type Page } from "@playwright/test"

const homeServerURLs = {
  default: "http://127.0.0.1:3100",
  catalogNotPublished: "http://127.0.0.1:3101",
  upstreamError: "http://127.0.0.1:3102",
} as const

async function waitForNuxtHydration(page: Page) {
  await expect(page.locator(".app-header__menu-button")).toBeEnabled()
}

async function gotoSSRHome(page: Page, baseURL: string) {
  const response = await page.goto(baseURL)

  if (response === null) {
    throw new Error(`Expected an HTML document from ${baseURL}`)
  }
  expect(response.request().resourceType()).toBe("document")
  expect(response.status()).toBe(200)

  return response.text()
}

test.describe("first Home SSR states without client JavaScript", () => {
  test.use({ javaScriptEnabled: false })

  test("renders catalog_not_published as empty state in the first document HTML", async ({
    page,
  }) => {
    const html = await gotoSSRHome(
      page,
      homeServerURLs.catalogNotPublished,
    )

    expect(html).toContain('data-state="empty"')
    expect(html).toContain("等待首次同步")
    expect(html).toContain("公开目录尚未发布")
    expect(html).not.toContain('data-home-section="formal-publications"')
  })

  test("renders an upstream Home API error in the first document HTML", async ({
    page,
  }) => {
    const html = await gotoSSRHome(page, homeServerURLs.upstreamError)

    expect(html).toContain('data-state="error"')
    expect(html).toContain("暂时无法加载")
    expect(html).toContain("重试")
    expect(html).toContain("00000000-0000-4000-8000-000000000901")
    expect(html).not.toContain('data-home-section="formal-publications"')
  })
})

for (const viewport of [
  { height: 844, width: 390 },
  { height: 1024, width: 768 },
  { height: 1000, width: 1440 },
]) {
  test(`keeps the daily shell accessible and overflow-free at ${viewport.width}px`, async ({
    page,
  }) => {
    const consoleErrors: string[] = []
    const failedResponses: string[] = []
    const pageErrors: string[] = []
    page.on("console", (message) => {
      if (message.type() === "error") {
        consoleErrors.push(message.text())
      }
    })
    page.on("pageerror", (error) => {
      pageErrors.push(error.message)
    })
    page.on("response", (response) => {
      if (response.status() >= 400) {
        failedResponses.push(`${response.status()} ${response.url()}`)
      }
    })
    await page.setViewportSize(viewport)
    await page.goto("/")

    await expect(page.locator("h1")).toHaveText("medpaperhub 今日论文情报")
    await expect(page.locator('nav[aria-label="一级导航"]')).toBeAttached()
    const search = page.getByRole("link", { exact: true, name: "搜索论文" })
    await expect(search).toBeVisible()
    await expect(search).toHaveAttribute("href", "/papers#papers-q")
    await expect(page.locator(".app-header__route-name")).toHaveText("今日")
    await expect(page.locator(".discovery-rail")).toHaveCount(0)
    await expect(page.locator('form[role="search"]')).toHaveCount(0)
    await expect(page.locator("body")).not.toContainText(
      "医学生物学研究情报，从新论文到可验证趋势",
    )
    await expect(
      page.getByRole("heading", { level: 2, name: "今日正式发表" }),
    ).toBeVisible()
    await expect(
      page.getByRole("heading", { level: 2, name: "近期接收" }),
    ).toBeVisible()
    await expect(
      page.getByRole("heading", { level: 2, name: "在线优先" }),
    ).toBeVisible()
    await expect(page.locator("[data-home-section]").first()).toHaveAttribute(
      "data-home-section",
      "formal-publications",
    )
    await expect(page.locator("[data-home-section]")).toHaveCount(6)
    expect(
      await page
        .locator("[data-home-section]")
        .evaluateAll((sections) =>
          sections.map((section) => section.getAttribute("data-home-section")),
        ),
    ).toEqual([
      "formal-publications",
      "recent-acceptances",
      "recent-online-first",
      "trends",
      "journals",
      "subjects",
    ])
    await expect(page.locator("body")).toContainText(
      "Prospective oncology cohort with external validation",
    )

    const dimensions = await page.evaluate(() => ({
      clientWidth: document.documentElement.clientWidth,
      scrollWidth: document.documentElement.scrollWidth,
    }))

    expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth)

    const undersizedTargets = await page
      .locator("a:visible, button:visible, input:visible, summary:visible")
      .evaluateAll((elements) =>
        elements.flatMap((element) => {
          const rectangle = element.getBoundingClientRect()
          if (rectangle.width >= 44 && rectangle.height >= 44) {
            return []
          }
          return [
            {
              height: rectangle.height,
              label:
                element.getAttribute("aria-label")
                ?? element.textContent?.trim()
                ?? element.tagName,
              tag: element.tagName,
              width: rectangle.width,
            },
          ]
        }),
      )

    expect(undersizedTargets).toEqual([])

    const shellOverflow = await page
      .locator(".app-shell__content, .app-shell__main")
      .evaluateAll((elements) =>
        elements.map((element) => getComputedStyle(element).overflowY),
    )
    expect(shellOverflow).not.toContain("auto")
    expect(shellOverflow).not.toContain("scroll")
    expect(consoleErrors).toEqual([])
    expect(failedResponses).toEqual([])
    expect(pageErrors).toEqual([])
  })
}

test("restores focus after Escape closes the mobile navigation", async ({
  page,
}) => {
  await page.setViewportSize({ height: 900, width: 375 })
  await page.goto("/")
  await waitForNuxtHydration(page)
  const menuButton = page.locator(".app-header__menu-button")

  await menuButton.click()
  await expect(menuButton).toHaveAttribute("aria-expanded", "true")
  await expect(menuButton).toHaveAccessibleName("关闭一级导航")
  await expect(page.getByRole("navigation", { name: "一级导航" })).toBeVisible()

  await page.keyboard.press("Escape")

  await expect(menuButton).toHaveAttribute("aria-expanded", "false")
  await expect(menuButton).toBeFocused()
  await expect(page.locator("html")).not.toHaveClass(/menu-open/)
})

test("releases the mobile menu scroll lock at the desktop breakpoint", async ({
  page,
}) => {
  await page.setViewportSize({ height: 900, width: 375 })
  await page.goto("/")
  await waitForNuxtHydration(page)
  const menuButton = page.locator(".app-header__menu-button")

  await menuButton.click()
  await expect(page.locator("html")).toHaveClass(/menu-open/)

  await page.setViewportSize({ height: 900, width: 768 })

  await expect(page.locator("html")).not.toHaveClass(/menu-open/)
  await expect(page.getByRole("navigation", { name: "一级导航" })).toBeVisible()
  await expect(menuButton).toBeHidden()
})

test("reduces transition and animation durations when motion is reduced", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" })
  await page.goto("/")

  const durations = await page
    .getByRole("link", { name: "搜索论文" })
    .evaluate((element) => {
      const style = getComputedStyle(element)
      const toMilliseconds = (duration: string) => {
        const value = Number.parseFloat(duration)
        return duration.endsWith("ms") ? value : value * 1000
      }

      return {
        animation: style.animationDuration
          .split(",")
          .map((duration) => toMilliseconds(duration.trim())),
        transition: style.transitionDuration
          .split(",")
          .map((duration) => toMilliseconds(duration.trim())),
      }
    })

  expect(Math.max(...durations.animation)).toBeLessThanOrEqual(0.01)
  expect(Math.max(...durations.transition)).toBeLessThanOrEqual(0.01)
})

for (const route of [
  { heading: "论文", path: "/papers" },
  { heading: "Topic", path: "/topics" },
  { heading: "Method", path: "/methods" },
  { heading: "趋势", path: "/trends" },
  { heading: "研究机会", path: "/opportunities" },
]) {
  test(`renders the real API boundary for ${route.path}`, async ({ page }) => {
    await page.goto(route.path)
    await expect(
      page.getByRole("heading", { level: 1, name: route.heading }),
    ).toBeVisible()

    const state = page.locator(".data-state").first()
    if (await state.count()) {
      const stateKind = await state.getAttribute("data-state")
      expect(["empty", "error"]).toContain(stateKind)
      const title = await state.getByRole("heading", { level: 2 }).textContent()
      expect(["等待首次同步", "暂时无法加载"]).toContain(title)
      if (title === "暂时无法加载") {
        await expect(state.getByRole("button", { name: "重试" })).toBeVisible()
      }
    }

    await expect(page.locator("body")).not.toContainText("示例论文")
    await expect(page.locator("body")).not.toContainText("伪造趋势")
  })
}

test("restores the admitted biomedical paper filters from the shareable URL", async ({
  page,
}) => {
  await page.goto(
    "/papers?q=oncology&type=research_article&sort=relevance"
    + "&published_from=2026-07-01T00%3A00%3A00Z"
    + "&published_to=2026-07-16T23%3A59%3A59Z",
  )

  await expect(page.locator('[name="q"]')).toHaveValue("oncology")
  await expect(page.locator('[name="type"]')).toHaveValue("research_article")
  await expect(page.locator('[name="sort"]')).toHaveValue("relevance")
  await expect(page.locator('[name="published_from"]')).toHaveValue(
    "2026-07-01T00:00:00Z",
  )
  await expect(page.locator('[name="published_to"]')).toHaveValue(
    "2026-07-16T23:59:59Z",
  )
  await expect(page.locator('[name="type"] option')).toHaveText([
    "全部类型",
    "研究论文",
    "综述",
  ])
  for (const removed of [
    "topic",
    "method",
    "has_code",
    "has_data",
    "has_benchmark",
    "status",
    "source",
  ]) {
    await expect(page.locator(`[name="${removed}"]`)).toHaveCount(0)
  }
  await expect(page.locator('[name="jif_min"]')).toHaveCount(0)
  await expect(page.locator('[name="jcr_quartile"]')).toHaveCount(0)
})

test("submits paper filters as exact Go API query names", async ({ page }) => {
  await page.goto("/papers")
  const submitButton = page.getByRole("button", { name: "应用筛选" })
  await expect(submitButton).toBeEnabled()

  await page.locator('[name="q"]').fill("oncology cohort")
  await page.locator('[name="type"]').selectOption("review")
  await page.locator('[name="sort"]').selectOption("relevance")
  await submitButton.click()

  await expect
    .poll(() => new URL(page.url()).searchParams.get("q"))
    .toBe("oncology cohort")
  expect(new URL(page.url()).pathname).toBe("/papers")
  const query = new URL(page.url()).searchParams
  expect(Object.fromEntries(query)).toEqual({
    q: "oncology cohort",
    sort: "relevance",
    type: "review",
  })
})

for (const route of [
  {
    back: "返回论文目录",
    label: "论文详情",
    path: "/papers/00000000-0000-0000-0000-000000000000",
  },
  {
    back: "返回 Topic",
    label: "Topic",
    path: "/topics/not-published",
  },
  {
    back: "返回 Method",
    label: "Method",
    path: "/methods/not-published",
  },
]) {
  test(`keeps ${route.path} recoverable without invented detail data`, async ({
    page,
  }) => {
    await page.goto(route.path)

    await expect(page.locator(".app-header__route-name")).toHaveText(
      route.label,
    )
    await expect(page.getByRole("link", { name: route.back })).toBeVisible()
    await expect(page.locator("body")).not.toContainText("示例论文")
  })
}
