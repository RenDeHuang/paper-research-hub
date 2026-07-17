import { expect, test, type Page } from "@playwright/test"

async function waitForNuxtHydration(page: Page) {
  await expect(page.locator(".app-header__menu-button")).toBeEnabled()
}

for (const width of [375, 768, 1024, 1440]) {
  test(`keeps the shell accessible and overflow-free at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ height: 900, width })
    await page.goto("/")

    await expect(
      page.getByRole("heading", {
        level: 1,
        name: "医学生物学研究情报，从新论文到可验证趋势",
      }),
    ).toBeVisible()
    await expect(page.locator('nav[aria-label="一级导航"]')).toBeAttached()
    await expect(
      page.getByRole("link", { exact: true, name: "搜索" }),
    ).toBeVisible()
    await expect(page.locator(".app-header__route-name")).toHaveText("首页")

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

    const rail = page.locator(".discovery-rail")
    if (width < 1024) {
      await expect(rail).toBeHidden()
    } else {
      await expect(rail).toBeVisible()
      const railLayout = await rail.evaluate((element) => {
        const style = getComputedStyle(element)
        return {
          overflowY: style.overflowY,
          width: element.getBoundingClientRect().width,
        }
      })

      expect(railLayout.width).toBe(width >= 1440 ? 232 : 216)
      expect(["auto", "scroll"]).not.toContain(railLayout.overflowY)
    }

    const shellOverflow = await page
      .locator(".app-shell__content, .app-shell__main")
      .evaluateAll((elements) =>
        elements.map((element) => getComputedStyle(element).overflowY),
      )
    expect(shellOverflow).not.toContain("auto")
    expect(shellOverflow).not.toContain("scroll")
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
    .getByRole("button", { name: "搜索" })
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

test("loads a shareable search query and submits the trimmed default URL", async ({
  page,
}) => {
  await page.goto("/?q=shared%20query")
  await waitForNuxtHydration(page)
  const search = page.getByRole("combobox", {
    name: "搜索医学生物学论文",
  })

  await expect(search).toHaveValue("shared query")
  await search.fill("  agent systems  ")
  await page.getByRole("button", { name: "搜索" }).click()

  await expect(page).toHaveURL(/\/papers\?q=agent(\+|%20)systems$/)
})

test("restores the homepage search query through browser history", async ({
  page,
}) => {
  const search = page.getByRole("combobox", {
    name: "搜索医学生物学论文",
  })

  await page.goto("/?q=first")
  await waitForNuxtHydration(page)
  await expect(search).toHaveValue("first")

  await page.goto("/?q=second")
  await waitForNuxtHydration(page)
  await expect(search).toHaveValue("second")

  await page.goBack()
  await expect(page).toHaveURL(/\/\?q=first$/)
  await expect(search).toHaveValue("first")

  await page.goForward()
  await expect(page).toHaveURL(/\/\?q=second$/)
  await expect(search).toHaveValue("second")
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
