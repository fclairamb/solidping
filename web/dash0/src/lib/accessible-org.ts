import type { OrganizationSummary } from "@/contexts/AuthContext";

/**
 * The session half of what {@link pickAccessibleOrg} needs: the org the current
 * token is scoped to, every org this user is a member of, and whether the user
 * is a super admin. Structural on purpose — `useAuth()`'s context value
 * satisfies it directly at both call sites, and a unit test can pass a literal.
 */
export interface AccessibleOrgSession {
  /** The org the current access token is scoped to (`auth.org`). */
  org: string | null;
  /** Every org this user belongs to (`auth.organizations`, from /auth/me). */
  organizations: OrganizationSummary[];
  /** `auth.user?.isSuperAdmin === true`. */
  isSuperAdmin: boolean;
}

/**
 * Picks the org this session can actually use when the URL names `urlOrg`.
 *
 * Any session can land on an org URL it cannot use: a bookmark to an org you
 * were removed from, a link a colleague pasted from *their* org, the live demo
 * entered from another org's login page. Before spec 2026-09-08-01 that dead-
 * ended — every org-scoped request 403'd and `PermissionDenied`'s only button
 * linked back to the very same org. Nothing sent the user to an org they *can*
 * use, even though the client already holds the whole list.
 *
 * The order below mirrors what the backend does at login time
 * (`resolveDefaultOrg` in server/internal/handlers/auth/service.go): prefer the
 * org this browser used most recently, then the first membership.
 *
 * 1. **Super admin → `urlOrg`.** Super admins cross orgs on their claims alone
 *    and are never switched or redirected (see OrgLayout's `needsOrgSwitch`).
 * 2. **`urlOrg` is a membership → `urlOrg`.** The URL is fine as it stands;
 *    a token minted for another org is OrgLayout's switch case, not this one.
 * 3. **The session's org → `session.org`.** `auth.org` IS "last accessed": every
 *    login and every `switchOrg` re-mints the token for exactly one org. Only
 *    honored while it is still a membership — a user removed from the org their
 *    token names must not be sent there.
 * 4. **`organizations[0]`.** The same fallback the login path uses; the list is
 *    ordered `created_at DESC` (most recently joined first) and that order is
 *    what the org switcher and the login org picker already display.
 * 5. **`null`** — this session belongs to no organization at all. The caller
 *    sends it to `/no-org`.
 *
 * Pure: no React, no network, no storage. Both redirect call sites (the login
 * page's already-authenticated effect and OrgLayout's non-member guard) route
 * through it so the two can never disagree and bounce a user between them.
 */
export function pickAccessibleOrg(
  urlOrg: string,
  session: AccessibleOrgSession,
): string | null {
  if (session.isSuperAdmin) return urlOrg;

  const isMember = (slug: string | null): slug is string =>
    slug !== null && session.organizations.some((o) => o.slug === slug);

  if (isMember(urlOrg)) return urlOrg;
  if (isMember(session.org)) return session.org;

  return session.organizations[0]?.slug ?? null;
}
