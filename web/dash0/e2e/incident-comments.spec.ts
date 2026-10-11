import { test, expect, DASH_BASE } from "./fixtures";

test.describe("Incident comments", () => {
  // Deterministically seeded in test mode (server/test/testdata): an active
  // incident used by the notifications test too.
  const incidentUid = "00000000-0000-0000-0000-000000000013";

  test("add a comment via the composer, see it in the discussion, rendered formatted (URL, inline code, fenced block)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`${DASH_BASE}/orgs/test/incidents/${incidentUid}`);
    await expect(page.getByText("Incident Details")).toBeVisible();

    // The Comments card renders with a composer.
    await expect(page.getByText("Comments", { exact: true })).toBeVisible();
    const composer = page.getByTestId("comment-input");
    await composer.scrollIntoViewIfNeeded();
    await expect(composer).toBeVisible();

    // Submit is disabled until there is text.
    const submit = page.getByTestId("comment-submit");
    await expect(submit).toBeDisabled();

    // Multi-line on purpose: the fenced block only renders if the line breaks
    // survive the round trip through the API.
    const marker = `E2E comment ${Date.now()}`;
    const commentText = [
      `${marker} confirmed on https://acme.com/status.`,
      "run `curl -I https://acme.com`",
      "```",
      "curl -I https://acme.com",
      "```",
    ].join("\n");

    await composer.fill(commentText);
    await expect(submit).toBeEnabled();
    await submit.click();

    // The new comment appears in the discussion list and the composer clears.
    const bubble = page
      .getByTestId("incident-comment")
      .filter({ hasText: marker });
    await expect(bubble).toBeVisible({ timeout: 10000 });
    await expect(composer).toHaveValue("");

    // URL autolinked, without swallowing the trailing period.
    const link = bubble.getByRole("link", {
      name: "https://acme.com/status",
    });
    await expect(link).toBeVisible();
    await expect(link).toHaveAttribute("href", "https://acme.com/status");
    await expect(link).toHaveAttribute("target", "_blank");
    await expect(link).toHaveAttribute("rel", "noopener noreferrer");
    await expect(bubble).toContainText("confirmed on https://acme.com/status.");

    // Inline code renders as <code>, outside of any <pre> (distinct from
    // the fenced block below, which also contains "curl -I").
    await expect(
      bubble.locator("span > code", { hasText: "curl -I" }),
    ).toBeVisible();

    // Fenced block renders as its own <pre>.
    const pre = bubble.locator("pre");
    await expect(pre).toBeVisible();
    await expect(pre).toContainText("curl -I https://acme.com");

    await page.screenshot({
      path: "test-results/screenshots/incident-comment-added.png",
      fullPage: true,
    });
  });
});
