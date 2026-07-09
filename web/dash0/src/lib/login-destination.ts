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
  if (
    returnTo &&
    isSafeReturnTo(returnTo, basepath) &&
    returnToOrg(returnTo, basepath) === resolvedOrg
  ) {
    return { href: returnTo };
  }
  return { to: "/orgs/$org", params: { org: resolvedOrg } };
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
