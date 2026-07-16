import { expect, test } from "@playwright/test"

for (const width of [375, 768, 1024, 1440]) {
  test(`renders the visual shell without horizontal overflow at ${width}px`, async ({
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

    const dimensions = await page.evaluate(() => ({
      clientWidth: document.documentElement.clientWidth,
      scrollWidth: document.documentElement.scrollWidth,
    }))

    expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth)
  })
}
