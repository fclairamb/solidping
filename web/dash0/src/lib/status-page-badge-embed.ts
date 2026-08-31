/**
 * Builds the public-page link and the copyable Markdown/HTML badge embed
 * snippets for `StatusPageBadgeCard` (spec 2026-08-31-03).
 *
 * Both snippets used to emit a bare `<img>` / `![]()` — an image with no
 * link, which defeats the badge's whole purpose ("click here to see whether
 * we are up"). They now wrap the badge image in a link to the public status
 * page, mirroring the preview link already added by spec 2026-08-30-01
 * (`status-page-badge-card.tsx`, `data-testid="status-page-badge-preview-link"`).
 *
 * Pulled out of the component into a pure function so the escaping rules
 * below — the actual bug fixed here — can be pinned with a plain unit test
 * instead of a DOM-rendering one. See `status-page-badge-embed.test.ts`.
 */

/**
 * Escapes text for use inside a double-quoted HTML attribute value.
 * `pageName` is operator-controlled free text that lands verbatim in
 * `alt="..."` — an unescaped `"` would break out of the attribute and
 * corrupt the snippet.
 */
export function escapeHtmlAttribute(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/"/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

/**
 * Escapes text for use as Markdown alt text inside `![...]()`. A bare `[`
 * or `]` would otherwise prematurely close the alt-text brackets and
 * corrupt the link syntax.
 */
export function escapeMarkdownAltText(value: string): string {
  return value.replace(/[[\]]/g, "\\$&");
}

export interface StatusPageBadgeEmbedSnippets {
  /** Path to the public status page, relative — reused for the in-app preview link. */
  pagePath: string;
  /** Absolute URL to the public status page, sharing `badgeUrl`'s origin. */
  pageUrl: string;
  /** Markdown snippet: a linked badge image, `[![alt](badge)](page)`. */
  markdownCode: string;
  /** HTML snippet: `<img>` wrapped in an `<a>` that opens in a new tab safely. */
  htmlCode: string;
}

export function buildStatusPageBadgeEmbedSnippets({
  origin,
  org,
  pageSlug,
  pageName,
  badgeUrl,
}: {
  /** `window.location.origin` — kept as a param so this stays a pure, testable function. */
  origin: string;
  org: string;
  pageSlug: string;
  pageName: string;
  /** The already-built absolute badge image URL (includes any label/style query params). */
  badgeUrl: string;
}): StatusPageBadgeEmbedSnippets {
  const pagePath = `/status0/${org}/${pageSlug}`;
  const pageUrl = `${origin}${pagePath}`;
  const markdownCode = `[![${escapeMarkdownAltText(pageName)} status](${badgeUrl})](${pageUrl})`;
  const htmlCode = `<a href="${pageUrl}" target="_blank" rel="noopener noreferrer"><img src="${badgeUrl}" alt="${escapeHtmlAttribute(pageName)} status" /></a>`;
  return { pagePath, pageUrl, markdownCode, htmlCode };
}
