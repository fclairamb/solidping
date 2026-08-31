import { describe, expect, it } from "vitest";

import {
  buildStatusPageBadgeEmbedSnippets,
  escapeHtmlAttribute,
  escapeMarkdownAltText,
} from "./status-page-badge-embed";

/**
 * Spec 2026-08-31-03: the badge embed snippets used to paste a bare image
 * with no link — clicking a status badge on a customer's site did nothing.
 * These tests pin the exact snippet TEXT, since that string is what a user
 * copies verbatim into their README/footer.
 */
describe("buildStatusPageBadgeEmbedSnippets", () => {
  const base = {
    origin: "https://status.acme.com",
    org: "acme",
    pageSlug: "public",
    pageName: "Acme",
    badgeUrl: "https://status.acme.com/api/v1/status-pages/acme/public/badge",
  };

  it("derives an absolute page URL sharing the badge URL's origin", () => {
    const { pagePath, pageUrl } = buildStatusPageBadgeEmbedSnippets(base);
    expect(pagePath).toBe("/status0/acme/public");
    expect(pageUrl).toBe("https://status.acme.com/status0/acme/public");
  });

  it("wraps the Markdown snippet's badge image in a link to the page", () => {
    const { markdownCode, pageUrl } = buildStatusPageBadgeEmbedSnippets(base);
    expect(markdownCode).toBe(`[![Acme status](${base.badgeUrl})](${pageUrl})`);
  });

  it("wraps the HTML snippet's <img> in a safe, new-tab <a>", () => {
    const { htmlCode, pageUrl } = buildStatusPageBadgeEmbedSnippets(base);
    expect(htmlCode).toBe(
      `<a href="${pageUrl}" target="_blank" rel="noopener noreferrer"><img src="${base.badgeUrl}" alt="Acme status" /></a>`,
    );
    expect(htmlCode).toContain('rel="noopener noreferrer"');
    expect(htmlCode).toContain('target="_blank"');
  });

  it("keeps a custom label/style flowing through badgeUrl unchanged", () => {
    const customBadgeUrl = `${base.badgeUrl}?label=Custom&style=flat-square`;
    const { markdownCode, htmlCode } = buildStatusPageBadgeEmbedSnippets({
      ...base,
      badgeUrl: customBadgeUrl,
    });
    expect(markdownCode).toContain(`(${customBadgeUrl})`);
    expect(htmlCode).toContain(`src="${customBadgeUrl}"`);
  });

  it("still yields a parseable Markdown snippet when the page name contains ]", () => {
    const { markdownCode } = buildStatusPageBadgeEmbedSnippets({
      ...base,
      pageName: "All ] status",
    });
    expect(markdownCode).toBe(
      `[![All \\] status status](${base.badgeUrl})](https://status.acme.com/status0/acme/public)`,
    );
  });

  it("still yields a parseable Markdown snippet when the page name contains [", () => {
    const { markdownCode } = buildStatusPageBadgeEmbedSnippets({
      ...base,
      pageName: "[Acme]",
    });
    expect(markdownCode).toBe(
      `[![\\[Acme\\] status](${base.badgeUrl})](https://status.acme.com/status0/acme/public)`,
    );
  });

  it("still yields a parseable HTML snippet when the page name contains a double quote", () => {
    const { htmlCode } = buildStatusPageBadgeEmbedSnippets({
      ...base,
      pageName: 'Acme "Prod"',
    });
    expect(htmlCode).toContain('alt="Acme &quot;Prod&quot; status"');
    // No stray unescaped quote breaks the alt attribute out early.
    expect(htmlCode).not.toContain('alt="Acme "Prod"');
  });
});

describe("escapeHtmlAttribute", () => {
  it("escapes double quotes, angle brackets and ampersands", () => {
    expect(escapeHtmlAttribute('Acme "Prod" <team> & co')).toBe(
      "Acme &quot;Prod&quot; &lt;team&gt; &amp; co",
    );
  });

  it("leaves plain text untouched", () => {
    expect(escapeHtmlAttribute("Acme Status")).toBe("Acme Status");
  });
});

describe("escapeMarkdownAltText", () => {
  it("escapes square brackets", () => {
    expect(escapeMarkdownAltText("All ] status [now]")).toBe(
      "All \\] status \\[now\\]",
    );
  });

  it("leaves plain text untouched", () => {
    expect(escapeMarkdownAltText("Acme Status")).toBe("Acme Status");
  });
});
