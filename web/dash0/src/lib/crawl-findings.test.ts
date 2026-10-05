import { describe, expect, it } from "vitest";
import { crawlProgress, isCrawlOutput, summarizeCrawlOutput } from "./crawl-findings";

const finding = (type: string, url: string, fingerprint: string) => ({
  type,
  url,
  source: "https://www.acme.com/",
  fingerprint,
});

describe("summarizeCrawlOutput", () => {
  it("groups findings by type in report order and flags the new ones", () => {
    const output = {
      counts: { broken_link: 2, sitemap_error: 1, mixed_content_passive: 0 },
      findings: [
        finding("sitemap_error", "https://www.acme.com/gone", "c"),
        finding("broken_link", "https://www.acme.com/a", "a"),
        finding("broken_link", "https://www.acme.com/b", "b"),
      ],
      findingsTotal: 3,
      newFindings: { count: 1, items: [finding("broken_link", "https://www.acme.com/b", "b")] },
      pagesCrawled: 12,
      incomplete: false,
    };

    const summary = summarizeCrawlOutput(output);

    expect(isCrawlOutput(output)).toBe(true);
    expect(summary.groups.map((g) => g.type)).toEqual(["broken_link", "sitemap_error"]);
    expect(summary.groups[0].count).toBe(2);
    expect(summary.groups[0].findings.map((f) => f.isNew)).toEqual([false, true]);
    expect(summary.newCount).toBe(1);
    expect(summary.pagesCrawled).toBe(12);
    expect(summary.total).toBe(3);
  });

  it("marks every finding new on a first run", () => {
    const output = {
      counts: { broken_link: 1 },
      findings: [finding("broken_link", "https://www.acme.com/a", "a")],
      findingsTotal: 1,
      newFindings: { count: 1, items: [] },
    };

    expect(summarizeCrawlOutput(output).groups[0].findings[0].isNew).toBe(true);
  });

  it("keeps a count beyond the first 50 findings", () => {
    const output = { counts: { broken_link: 120 }, findings: [], findingsTotal: 120 };
    const summary = summarizeCrawlOutput(output);

    expect(summary.groups[0].count).toBe(120);
    expect(summary.shown).toBe(0);
  });

  it("is empty for a non-crawl output", () => {
    expect(isCrawlOutput({ status_code: 200 })).toBe(false);
    expect(summarizeCrawlOutput(undefined).groups).toEqual([]);
  });
});

describe("crawlProgress", () => {
  it("reads pages done and max pages", () => {
    expect(crawlProgress({ pagesDone: 42, maxPages: 200 })).toEqual({ done: 42, max: 200 });
    expect(crawlProgress({ queued: 3 })).toBeNull();
    expect(crawlProgress(undefined)).toBeNull();
  });
});
