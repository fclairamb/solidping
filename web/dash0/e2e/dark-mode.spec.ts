import { test, expect } from "./fixtures";

test.describe("Dark Mode Persistence", () => {
  test("should persist dark mode across page reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    // Click theme toggle button (in the header, not a menu)
    await page.getByTestId("theme-toggle").click();

    // Verify dark class is applied
    const isDark = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(isDark).toBe(true);

    // Verify localStorage was set
    const storedTheme = await page.evaluate(() =>
      localStorage.getItem("theme"),
    );
    expect(storedTheme).toBe("dark");

    // Reload and verify dark mode persists
    await page.reload();
    await page.waitForLoadState("domcontentloaded");

    const isDarkAfterReload = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(isDarkAfterReload).toBe(true);
  });
});
