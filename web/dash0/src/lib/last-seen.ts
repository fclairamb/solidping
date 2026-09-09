// Derives the members table's single "Last seen" headline value from the
// two DELIBERATELY separate timestamps the API returns
// (`lastSessionActivityAt` / `lastTokenActivityAt` on `MemberResponse` —
// see server/internal/handlers/members/service.go and
// wiki/api-specification/orgs.md). The API never pre-merges them so a
// departed employee whose automation still runs doesn't read as "active";
// this helper does the merge purely for the one-column display, keeping
// both raw values available in the API for a future two-column layout.

export type LastSeenVia = "session" | "token";

export interface LastSeenInput {
  lastSessionActivityAt?: string | null;
  lastTokenActivityAt?: string | null;
}

export interface LastSeenResult {
  /** ISO timestamp of the more recent of the two channels, or null when
   * neither has ever happened. */
  lastSeenAt: string | null;
  /** Which channel produced `lastSeenAt`. Null iff `lastSeenAt` is null. */
  lastSeenVia: LastSeenVia | null;
}

/**
 * Combines the two raw activity fields into one headline value + channel.
 * A tie (identical timestamps) goes to `session` — dashboard presence is the
 * more legible signal to lead with when both happened at once.
 */
export function lastSeenFor(input: LastSeenInput): LastSeenResult {
  const session = input.lastSessionActivityAt ?? null;
  const token = input.lastTokenActivityAt ?? null;

  if (session === null && token === null) {
    return { lastSeenAt: null, lastSeenVia: null };
  }

  if (session === null) {
    return { lastSeenAt: token, lastSeenVia: "token" };
  }

  if (token === null) {
    return { lastSeenAt: session, lastSeenVia: "session" };
  }

  const sessionMs = new Date(session).getTime();
  const tokenMs = new Date(token).getTime();

  return tokenMs > sessionMs
    ? { lastSeenAt: token, lastSeenVia: "token" }
    : { lastSeenAt: session, lastSeenVia: "session" };
}
