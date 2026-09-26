import type { OrganizationSummary } from "@/contexts/AuthContext";

/**
 * The session half of what {@link needsOrgSwitch} reads. Structural, like
 * `AccessibleOrgSession`: OrgLayout passes values off `useAuth()`, a unit test
 * passes a literal.
 */
export interface OrgSwitchSession {
  isAuthenticated: boolean;
  isLoading: boolean;
  /** The org the current access token is scoped to (`auth.org`), or null for an org-less session. */
  org: string | null;
  /** Every org this user belongs to (`auth.organizations`, from /auth/me). */
  organizations: OrganizationSummary[];
  isSuperAdmin: boolean;
}

/**
 * Whether OrgLayout must re-mint the session for `urlOrg` (`switchOrg()`)
 * before its children, and the live socket, may mount.
 *
 * Org access is token-scope equality on every surface (REST and the live WS):
 * a token minted for org A is refused on org B even for a genuine member (spec
 * 2026-07-15-01). So a member whose token is not scoped to the URL's org must
 * switch first, or the whole page 403s and the socket 4403s.
 *
 * That holds for an ORG-LESS token too (spec 2026-09-25-15). Such a session
 * has no refresh token, so the 403s escalate: the socket's refresh finds
 * nothing to spend, the session is cleared, and a real member is sent to the
 * login page as "session expired" a second after signing in. A token scoped to
 * no org is simply not scoped to this one.
 *
 * - Super admins cross orgs on their claims alone and never switch.
 * - Non-members never switch: that is `pickAccessibleOrg`'s redirect, and the
 *   two predicates are disjoint (a URL org in `organizations` always resolves
 *   to itself there).
 * - Nothing is decided while the session is still resolving, or on the org's
 *   own login/register pages (`isPublicRoute`).
 *
 * Pure: no React, no network, no storage.
 */
export function needsOrgSwitch(
  urlOrg: string,
  session: OrgSwitchSession,
  isPublicRoute: boolean,
): boolean {
  return (
    session.isAuthenticated &&
    !session.isLoading &&
    !isPublicRoute &&
    session.org !== urlOrg &&
    !session.isSuperAdmin &&
    session.organizations.some((o) => o.slug === urlOrg)
  );
}
