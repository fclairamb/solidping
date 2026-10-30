/**
 * Whether /no-org should offer "I'd rather join an existing organization".
 *
 * A signed-in user without an org gets a personal one automatically, so the
 * only people who still reach /no-org are those with something to join. The
 * join card is shown only when the user arrived through an explicit join path
 * (spec 2026-10-30-04):
 *
 * - a `?join=<slug>` search param;
 * - a `?membershipPending=<slug>` param (a federated sign-in the org refused);
 * - a pending or rejected membership request of theirs.
 */
export function shouldShowJoinCard(opts: {
  join?: string;
  membershipPending?: string;
  requests?: { status: string }[];
}): boolean {
  if (opts.join) return true;
  if (opts.membershipPending) return true;

  return (opts.requests ?? []).some(
    (r) => r.status === "pending" || r.status === "rejected",
  );
}
