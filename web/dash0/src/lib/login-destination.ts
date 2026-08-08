/**
 * Resolves where a just-authenticated user should land.
 *
 * Every post-login success path — the silent single-org redirect
 * (`orgRedirect`), the password/passkey/2FA `default` case, the multi-org
 * picker, and the already-authenticated effect — funnels through
 * {@link resolveDestination} so a deep link captured as `returnTo` on the way
 * *into* /login is honored on the way *out*, instead of dumping the user on
 * the org root.
 *
 * A `returnTo` is honored only when it is a safe, same-origin, in-app path
 * (`{basepath}/orgs/…`, never an absolute or protocol-relative URL) AND its
 * org segment matches the org the session actually resolved to. On any
 * mismatch or unsafe value it falls back to that org's root — we never send a
 * user to an org they did not log into, and never follow an open redirect.
 *
 * Two additional shapes are honored, both org-agnostic by construction:
 *
 * - the embedded MCP OAuth authorization endpoint (`/api/v1/oauth/authorize?…`,
 *   see {@link isOAuthAuthorizeReturnTo}). The server bounces session-less
 *   /authorize requests through /login with that relative path as returnTo;
 *   honoring it resumes the MCP consent flow.
 * - the device-authorization verification page (`{basepath}/device?user_code=…`,
 *   see {@link isDeviceVerificationReturnTo}). That route is deliberately
 *   org-less — it is the short URL the CLI prints and a human retypes — and it
 *   resolves the org from the session AFTER login. Applying the org-match rule
 *   to it would drop the returnTo for every user whose org is not literally
 *   the slug the logged-out visitor happened to be bounced through, losing the
 *   pre-filled one-time code and breaking the approve-from-your-phone flow
 *   that the device grant exists for (spec 2026-08-08-02).
 */
export type LoginDestination =
  | { href: string }
  | { to: "/orgs/$org"; params: { org: string } };

/**
 * Picks the post-login destination for `resolvedOrg`.
 *
 * @param resolvedOrg the org slug the session actually resolved to
 * @param returnTo    the captured deep link (may include the app base path)
 * @param basepath    the app base path (`import.meta.env.VITE_BASE_URL || ""`)
 */
export function resolveDestination(
  resolvedOrg: string,
  returnTo: string | undefined | null,
  basepath: string,
): LoginDestination {
  // MCP OAuth consent bounce: the embedded authorization server
  // (server/internal/oauth/authorize.go redirectToLogin) sends a session-less
  // /authorize request here with a relative returnTo pointing back at itself.
  // Honor it without the org-match rule — the authorize endpoint derives the
  // org from the session claims, and the consent screen displays it.
  if (isOAuthAuthorizeReturnTo(returnTo)) {
    return { href: returnTo };
  }
  // Device-authorization consent bounce: the org-less /device route sends a
  // logged-out visitor to /login with itself as returnTo, then resolves the
  // org from the restored session. Honoring it without the org-match rule is
  // what keeps the one-time code pre-filled across the login.
  if (isDeviceVerificationReturnTo(returnTo, basepath)) {
    return { href: returnTo };
  }
  if (
    returnTo &&
    isSafeReturnTo(returnTo, basepath) &&
    returnToOrg(returnTo, basepath) === resolvedOrg
  ) {
    return { href: returnTo };
  }
  return { to: "/orgs/$org", params: { org: resolvedOrg } };
}

/** The embedded MCP OAuth authorization endpoint (relative, path-anchored). */
const OAUTH_AUTHORIZE_PATH = "/api/v1/oauth/authorize";

/**
 * True when `returnTo` is the embedded OAuth authorization endpoint — exactly
 * `/api/v1/oauth/authorize`, optionally with a query string. Path-anchored, so
 * it can never carry a scheme (`https:`) or a protocol-relative (`//host`)
 * form: navigating to it is same-origin by construction, no open-redirect
 * risk. Anything else (subpaths, absolute URLs, lookalikes) is rejected.
 */
export function isOAuthAuthorizeReturnTo(
  returnTo: string | undefined | null,
): returnTo is string {
  if (!returnTo) return false;
  return (
    returnTo === OAUTH_AUTHORIZE_PATH ||
    returnTo.startsWith(`${OAUTH_AUTHORIZE_PATH}?`)
  );
}

/** The org-less device-authorization verification route. */
const DEVICE_VERIFICATION_PATH = "/device";

/**
 * True when `returnTo` is exactly the device verification page under the app
 * base path, optionally with a query string (`?user_code=…`).
 *
 * Path-anchored on `{basepath}/device`, so — like
 * {@link isOAuthAuthorizeReturnTo} — it can never carry a scheme (`https:`) or
 * a protocol-relative (`//host`) form: navigating to it is same-origin by
 * construction. Subpaths and lookalikes (`/deviceX`, `/device/extra`) are
 * rejected.
 */
export function isDeviceVerificationReturnTo(
  returnTo: string | undefined | null,
  basepath: string,
): returnTo is string {
  if (!returnTo) return false;
  const path = `${basepath}${DEVICE_VERIFICATION_PATH}`;
  return returnTo === path || returnTo.startsWith(`${path}?`);
}

/**
 * Builds the returnTo the org-less /device route hands to /login: the device
 * page itself, carrying the one-time code so it survives the round trip.
 */
export function deviceVerificationReturnTo(
  basepath: string,
  userCode: string | undefined,
): string {
  const path = `${basepath}${DEVICE_VERIFICATION_PATH}`;
  return userCode
    ? `${path}?user_code=${encodeURIComponent(userCode)}`
    : path;
}

/**
 * True when `returnTo` is a same-origin relative path pointing at an in-app
 * org route under the app base path. Rejects absolute URLs (`https://…`),
 * protocol-relative (`//host`) and backslash-obfuscated (`/\host`) forms so
 * the value can never drive an open redirect.
 */
export function isSafeReturnTo(returnTo: string, basepath: string): boolean {
  // Protocol-relative ("//evil.com") or backslash-obfuscated ("/\evil.com").
  if (returnTo.startsWith("//") || returnTo.startsWith("/\\")) return false;
  // Absolute URL carrying a scheme ("https:", "javascript:", "data:"…).
  if (/^[a-z][a-z\d+.-]*:/i.test(returnTo)) return false;
  // Must be an in-app org path under the app base path.
  return returnTo.startsWith(`${basepath}/orgs/`);
}

/**
 * Extracts the org slug from the `/orgs/{slug}/…` segment of a `returnTo`
 * path (query/hash stripped, base path removed). Returns null when absent.
 */
export function returnToOrg(returnTo: string, basepath: string): string | null {
  const pathOnly = returnTo.split(/[?#]/)[0];
  const rest = pathOnly.startsWith(basepath)
    ? pathOnly.slice(basepath.length)
    : pathOnly;
  const match = rest.match(/^\/orgs\/([^/]+)/);
  return match ? match[1] : null;
}
