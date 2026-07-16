import { expect, test } from "@playwright/test"

test("renders the accessible replatform status", async ({ page }) => {
  await page.goto("/")

  await expect(page.getByRole("main")).toHaveAttribute(
    "aria-labelledby",
    "replatform-heading",
  )
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Go + Nuxt 重构中",
    }),
  ).toHaveAttribute("id", "replatform-heading")
})
