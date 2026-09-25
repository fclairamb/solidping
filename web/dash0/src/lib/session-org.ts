/** Where the dashboard remembers the org the current session is scoped to. */
export const SESSION_ORG_KEY = "solidping_org";

/**
 * Records the org a login or `/auth/me` response says the session is scoped
 * to, and returns what `auth.org` must become.
 *
 * An org-less response (no `organization`) CLEARS the stored slug and yields
 * null. Keeping the previous session's slug would make `auth.org` claim a
 * scope the token does not have: OrgLayout's `needsOrgSwitch` would then read
 * "already scoped to this org" and skip the switch-org the session needs, and
 * every org-scoped request would 403 (spec 2026-09-25-15).
 */
export function persistSessionOrg(
  organization: { slug?: string } | null | undefined,
  storage: Pick<Storage, "setItem" | "removeItem"> = localStorage,
): string | null {
  const slug = organization?.slug || null;
  if (slug) {
    storage.setItem(SESSION_ORG_KEY, slug);
  } else {
    storage.removeItem(SESSION_ORG_KEY);
  }
  return slug;
}
