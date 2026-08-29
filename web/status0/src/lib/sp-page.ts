/**
 * Reads the server-injected `<meta name="sp-page" content="org/slug">` tag.
 *
 * On a custom domain the server resolves the page from the request host and
 * stamps this tag, so the SPA renders that page in place without navigating
 * (the address bar stays on the custom host). Returns null on the
 * installation's own hosts, where pages are reached via the URL path instead.
 *
 * Shared by the "/" route and the "/tv" route: two readers of one bootstrap
 * contract is exactly how the two would drift on a malformed value.
 */
export function readSpPage(): { org: string; slug: string } | null {
  if (typeof document === "undefined") return null;

  const meta = document.querySelector('meta[name="sp-page"]');
  const content = meta?.getAttribute("content")?.trim();
  if (!content) return null;

  const slash = content.indexOf("/");
  if (slash <= 0 || slash >= content.length - 1) return null;

  return { org: content.slice(0, slash), slug: content.slice(slash + 1) };
}
