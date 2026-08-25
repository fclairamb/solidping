/**
 * True for the two org-level public routes, `/orgs/:org/login` and
 * `/orgs/:org/register`.
 *
 * Anchored on the *shape* of the path rather than on a bare
 * `.endsWith("/register")`: the loose version also matched nested
 * authenticated routes that happen to end in "register" (e.g.
 * `organization/private-locations/register`), wrongly treating them as public
 * and skipping the auth redirect.
 *
 * Deliberately derived from the pathname ALONE — never interpolating the
 * `$org` route param into the pattern. `useParams()` and `useLocation()` are
 * two independent subscriptions, so during an org-changing navigation on the
 * login page (the org picker redirecting `/orgs/default/login` →
 * `/orgs/test/login`) there is a render where the param already says `test`
 * while the pathname still says `default`. A param-interpolated test reads
 * false for that one render, OrgLayout swaps `<Outlet/>` for the full
 * authenticated shell, and the login component unmounts — losing
 * `showOrgPicker` and bouncing the user off the picker before they can
 * choose. That is what broke the MCP OAuth login → picker → resume-returnTo
 * flow.
 */
export function isOrgPublicRoute(pathname: string): boolean {
  return /\/orgs\/[^/]+\/(login|register)$/.test(pathname);
}
