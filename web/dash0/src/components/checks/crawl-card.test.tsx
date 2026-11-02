/**
 * @vitest-environment jsdom
 *
 * Spec 2026-11-02-03: a crawl finding's URL comes from the crawled site, so
 * it is a link only when it is http(s); anything else renders as plain text.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";

vi.mock("@/api/hooks", () => ({
  useCheckRun: () => ({ data: undefined }),
  useCrawlReports: () => ({ data: [] }),
  useCancelCheckRun: () => ({ mutate: vi.fn(), isPending: false }),
}));

const { CrawlCard } = await import("./crawl-card");

afterEach(cleanup);

function finding(url: string, fingerprint: string) {
  return { type: "sitemap_error", url, fingerprint, error: "fetch failed" };
}

describe("CrawlCard finding URLs", () => {
  it("links http(s) URLs and renders other schemes as plain text", () => {
    render(
      <CrawlCard
        org="acme"
        checkUid="check-1"
        canCancel={false}
        output={{
          findings: [
            finding("https://www.acme.com/missing.xml", "a"),
            finding("javascript:alert(document.cookie)", "b"),
            finding("data:text/html,<script>alert(1)</script>", "c"),
            finding("HTTP://www.acme.com/upper.xml", "d"),
          ],
        }}
      />,
    );

    const urls = screen.getAllByTestId("crawl-finding-url");
    expect(urls).toHaveLength(4);

    const byText = (text: string) => urls.find((el) => el.textContent === text)!;

    const https = byText("https://www.acme.com/missing.xml");
    expect(https.tagName).toBe("A");
    expect(https.getAttribute("href")).toBe("https://www.acme.com/missing.xml");
    expect(byText("HTTP://www.acme.com/upper.xml").tagName).toBe("A");

    for (const text of ["javascript:alert(document.cookie)", "data:text/html,<script>alert(1)</script>"]) {
      const el = byText(text);
      expect(el.tagName).toBe("SPAN");
      expect(el.hasAttribute("href")).toBe(false);
      expect(el.closest("a")).toBeNull();
    }
    expect(document.querySelector('a[href^="javascript:"]')).toBeNull();
    expect(document.querySelector('a[href^="data:"]')).toBeNull();
  });
});
