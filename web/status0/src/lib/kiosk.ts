/**
 * Kiosk token handling for TV mode (spec 2026-08-29-08).
 *
 * A wallboard reaches a `password` or `private` status page by carrying the
 * page's kiosk token in the URL: `.../tv?kiosk=<token>`. That is the only
 * transport a TV stick can produce — somebody types a URL once and never
 * touches the device again — but it means the secret is, for a moment, printed
 * on a screen in a room people walk through.
 *
 * So the token is read ONCE, kept in memory, and erased from the address bar
 * with history.replaceState. Every subsequent poll re-attaches it from memory.
 * This is not a security boundary (anyone who saw the initial URL has the
 * token, and it is in the browser's history), but it removes the case that
 * actually happens: the URL sitting legible on a wall for months.
 *
 * Deliberately NOT persisted to localStorage or a cookie. A wallboard is
 * configured once and reloads from its own URL; persisting would instead mean
 * a revoked token quietly surviving in a browser profile, and a shared kiosk
 * machine carrying one page's grant into another tab.
 */

/** Query parameter carrying the token. Must match statuspagekiosk.QueryParam. */
export const KIOSK_PARAM = "kiosk";

let inMemoryToken: string | undefined;

/**
 * Reads the token from the current URL (if any), remembers it, and strips it
 * from the visible address bar.
 *
 * Idempotent and safe to call on every render: once the parameter is gone the
 * remembered value is simply returned. Returns undefined when no token was
 * ever presented, which is the normal case for a public page.
 */
export function captureKioskToken(): string | undefined {
  if (typeof window === "undefined") return inMemoryToken;

  const url = new URL(window.location.href);
  const fromUrl = url.searchParams.get(KIOSK_PARAM);

  if (!fromUrl) return inMemoryToken;

  inMemoryToken = fromUrl;
  url.searchParams.delete(KIOSK_PARAM);

  // replaceState rather than pushState: the tokenless URL replaces the
  // tokened one outright, so a Back press cannot put the secret back on screen.
  window.history.replaceState(
    window.history.state,
    "",
    url.pathname + url.search + url.hash,
  );

  return inMemoryToken;
}

/** The remembered token, without touching the URL. */
export function kioskToken(): string | undefined {
  return inMemoryToken;
}

/** Test seam — resets the module's memory between cases. */
export function resetKioskToken(): void {
  inMemoryToken = undefined;
}

/**
 * Appends the kiosk token to an API path when one is held.
 *
 * Centralized so no call site can forget it: a TV board that fetched the page
 * with the token but the incident history without it would render a locked
 * board with an empty incident list, which reads as "no incidents" — the exact
 * false reassurance this whole feature exists to avoid.
 */
export function withKiosk(path: string, token: string | undefined): string {
  if (!token) return path;

  const separator = path.includes("?") ? "&" : "?";

  return `${path}${separator}${KIOSK_PARAM}=${encodeURIComponent(token)}`;
}
