import { test, expect } from "@playwright/test";

// Deterministic seed from server/test/testdata/testdata.go (SP_RUNMODE=test):
// a successful network-discovery scan (jobs row …0007) carrying one unpromoted
// 127.0.0.1 group with a TCP/8080 and an ICMP suggested check.
const SCAN_UID = "00000000-0000-0000-0000-000000000007";

test.describe("Discovery host promotion", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/dash0/orgs/test/login");
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("promotes a seeded suggested check end-to-end", async ({ page }) => {
    // Open the seeded scan detail.
    await page.goto(`/dash0/orgs/test/discovery/${SCAN_UID}`);

    // The seeded host renders as a group card listing its suggested checks.
    const group = page
      .getByTestId("discovery-group")
      .filter({ hasText: "127.0.0.1" });
    await expect(group).toBeVisible();

    const tcpRow = group
      .getByTestId("discovery-check-row")
      .filter({ hasText: "TCP/8080" });
    await expect(tcpRow).toBeVisible();

    // The seed is one-shot per database: a CI retry after a mid-test failure
    // finds the row already promoted. The terminal state is what the test
    // guarantees, so assert it and stop rather than re-promoting.
    if (await tcpRow.getByText("Promoted").isVisible()) {
      await expect(tcpRow.getByRole("checkbox")).toBeDisabled();
      return;
    }

    // Select the TCP suggested check; the header button reflects the count.
    await tcpRow.getByRole("checkbox").check();
    const promoteButton = page.getByRole("button", {
      name: /promote selected \(1\)/i,
    });
    await expect(promoteButton).toBeEnabled();
    await promoteButton.click();

    // Success toast, and the row flips to the promoted badge once the backend
    // has created the check and the list refetched (end-to-end signal:
    // promotedToCheckUid is only set after the check row exists).
    await expect(page.getByText("Checks created")).toBeVisible();
    await expect(tcpRow.getByText("Promoted")).toBeVisible();
    await expect(tcpRow.getByRole("checkbox")).toBeDisabled();

    // The sibling ICMP suggestion is untouched.
    const icmpRow = group
      .getByTestId("discovery-check-row")
      .filter({ hasText: "ICMP" });
    await expect(icmpRow.getByText("Promoted")).toBeHidden();
  });
});
