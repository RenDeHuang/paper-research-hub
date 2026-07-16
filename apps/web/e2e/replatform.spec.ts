import { expect, test } from "@playwright/test"

for (const width of [375, 768, 1024, 1440]) {
  test(`keeps the shell accessible and overflow-free at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ height: 900, width })
    await page.goto("/")

    await expect(
      page.getByRole("heading", {
        level: 1,
        name: "论文研究情报，从发现到证据",
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
  await page.waitForFunction(() =>
    Boolean(
      (
        document.querySelector("#__nuxt") as
          | (Element & { __vue_app__?: unknown })
          | null
      )?.__vue_app__,
    ),
  )
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
