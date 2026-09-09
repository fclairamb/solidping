import { describe, expect, it } from "vitest";

import { lastSeenFor } from "@/lib/last-seen";

describe("lastSeenFor", () => {
  it("returns null/null when both are absent", () => {
    expect(lastSeenFor({})).toEqual({ lastSeenAt: null, lastSeenVia: null });
    expect(
      lastSeenFor({ lastSessionActivityAt: null, lastTokenActivityAt: null }),
    ).toEqual({ lastSeenAt: null, lastSeenVia: null });
  });

  it("uses the session value when only session activity exists", () => {
    const sessionAt = "2026-09-01T10:00:00Z";
    expect(lastSeenFor({ lastSessionActivityAt: sessionAt })).toEqual({
      lastSeenAt: sessionAt,
      lastSeenVia: "session",
    });
  });

  it("uses the token value when only token activity exists", () => {
    const tokenAt = "2026-09-01T10:00:00Z";
    expect(lastSeenFor({ lastTokenActivityAt: tokenAt })).toEqual({
      lastSeenAt: tokenAt,
      lastSeenVia: "token",
    });
  });

  it("picks the later timestamp when session is newer", () => {
    const sessionAt = "2026-09-05T10:00:00Z";
    const tokenAt = "2026-09-01T10:00:00Z";
    expect(
      lastSeenFor({ lastSessionActivityAt: sessionAt, lastTokenActivityAt: tokenAt }),
    ).toEqual({ lastSeenAt: sessionAt, lastSeenVia: "session" });
  });

  it("picks the later timestamp when token is newer", () => {
    const sessionAt = "2026-09-01T10:00:00Z";
    const tokenAt = "2026-09-05T10:00:00Z";
    expect(
      lastSeenFor({ lastSessionActivityAt: sessionAt, lastTokenActivityAt: tokenAt }),
    ).toEqual({ lastSeenAt: tokenAt, lastSeenVia: "token" });
  });

  it("breaks an exact tie in favor of session", () => {
    const at = "2026-09-01T10:00:00Z";
    expect(
      lastSeenFor({ lastSessionActivityAt: at, lastTokenActivityAt: at }),
    ).toEqual({ lastSeenAt: at, lastSeenVia: "session" });
  });
});
