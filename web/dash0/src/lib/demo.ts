/**
 * Client-side helpers for the shared public live demo (spec 2026-09-06-02).
 *
 * NONE OF THIS IS A SECURITY CONTROL. The server's write guard
 * (handlers/auth/demo_guard.go) and the checks service's ownership rule are
 * what actually refuse a demo write; everything here is politeness — not
 * offering a button whose only outcome would be a refusal toast.
 *
 * Keeping the rules in one small module means the several surfaces that need
 * them (the check list's row actions, the check form's type picker, the
 * read-only notes on settings pages) cannot drift apart from each other, and
 * that they can be unit-tested without a browser.
 */

/**
 * The check types a demo session may create. MUST stay in sync with
 * `demoAllowedCheckTypes` in server/internal/handlers/checks/demo.go — the
 * server is the authority, and a type listed here but refused there just moves
 * the refusal from a hidden option to an error toast.
 */
export const DEMO_ALLOWED_CHECK_TYPES = [
  "http",
  "tcp",
  "icmp",
  "dns",
  "ssl",
] as const;

/** The shortest period a demo-created check may run at, in seconds. */
export const DEMO_MIN_PERIOD_SECONDS = 60;

/**
 * Whether a demo session may edit or delete this check.
 *
 * A check with no creator — the seeded catalogue — is editable by nobody in the
 * demo, which is exactly how the server keeps it immutable without any
 * "protected" flag. A non-demo session is never restricted here: the ownership
 * rule is a property of the demo, not of the product.
 */
export function canDemoEditCheck(
  isDemo: boolean | undefined,
  currentUserUid: string | undefined,
  checkCreatedBy: string | null | undefined,
): boolean {
  if (!isDemo) return true;
  if (!checkCreatedBy || !currentUserUid) return false;

  return checkCreatedBy === currentUserUid;
}

/**
 * Filters a list of check types down to what a demo session may create.
 * Returns the list untouched for an ordinary session.
 */
export function filterCheckTypesForDemo<T extends { type?: string; name?: string }>(
  isDemo: boolean | undefined,
  types: T[],
): T[] {
  if (!isDemo) return types;

  const allowed = new Set<string>(DEMO_ALLOWED_CHECK_TYPES);

  return types.filter((entry) => allowed.has(entry.type ?? entry.name ?? ""));
}
