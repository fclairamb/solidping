import { describe, test, expect } from "bun:test";
import {
  STALE_AFTER_MS,
  cycleWindow,
  daysSince,
  durationParts,
  elapsedMs,
  isStale,
  lastResolvedAt,
  pollIntervalMs,
  recentResolved,
  resolveTvState,
} from "./tv-board";
import type { PublicIncident } from "@/api/hooks";

function incident(partial: Partial<PublicIncident>): PublicIncident {
  return {
    uid: partial.uid ?? "uid",
    title: partial.title ?? "Incident",
    state: partial.state ?? "investigating",
    startedAt: partial.startedAt ?? "2026-08-29T10:00:00Z",
    ...partial,
  };
}

describe("resolveTvState", () => {
  test("mirrors the server rollup when nothing is published", () => {
    expect(resolveTvState("operational", [])).toBe("operational");
    expect(resolveTvState("degraded", [])).toBe("degraded");
    expect(resolveTvState("down", [])).toBe("down");
    expect(resolveTvState("maintenance", [])).toBe("maintenance");
  });

  test("an unrecognised rollup is unknown, never green", () => {
    expect(resolveTvState(undefined, [])).toBe("unknown");
    expect(resolveTvState("", [])).toBe("unknown");
    expect(resolveTvState("something-new", [])).toBe("unknown");
  });

  // The single most important behaviour on this board: an operator published
  // an incident precisely because they know something the probes do not.
  test("an active critical publication turns a green board red", () => {
    expect(
      resolveTvState("operational", [incident({ severity: "critical" })]),
    ).toBe("down");
  });

  test("major and minor escalate a green board to at least degraded", () => {
    expect(
      resolveTvState("operational", [incident({ severity: "major" })]),
    ).toBe("degraded");
    expect(
      resolveTvState("operational", [incident({ severity: "minor" })]),
    ).toBe("degraded");
  });

  test("an ungraded publication still shows, without inventing a severity", () => {
    expect(resolveTvState("operational", [incident({})])).toBe("degraded");
  });

  test("publications escalate but never de-escalate", () => {
    expect(resolveTvState("down", [incident({ severity: "minor" })])).toBe(
      "down",
    );
  });

  test("the worst of several publications wins", () => {
    expect(
      resolveTvState("operational", [
        incident({ uid: "a", severity: "minor" }),
        incident({ uid: "b", severity: "critical" }),
        incident({ uid: "c", severity: "major" }),
      ]),
    ).toBe("down");
  });

  // A planned window must never paint the office red at 03:00, or the room
  // learns to ignore the screen.
  test("a maintenance window on its own stays blue", () => {
    expect(resolveTvState("maintenance", [])).toBe("maintenance");
    expect(resolveTvState("maintenance", undefined)).toBe("maintenance");
  });

  test("...but a real incident during maintenance still escalates", () => {
    expect(
      resolveTvState("maintenance", [incident({ severity: "critical" })]),
    ).toBe("down");
  });
});

describe("isStale", () => {
  const now = 1_000_000_000_000;

  test("fresh data is not stale", () => {
    expect(isStale(now - 10_000, now)).toBe(false);
  });

  test("three missed polls flip the board", () => {
    expect(isStale(now - STALE_AFTER_MS, now)).toBe(true);
    expect(isStale(now - STALE_AFTER_MS - 1, now)).toBe(true);
    expect(isStale(now - STALE_AFTER_MS + 1, now)).toBe(false);
  });

  // The initial load is not a data outage — the board shows its loading state
  // rather than shouting about data it has not asked for yet.
  test("never having polled is not stale", () => {
    expect(isStale(undefined, now)).toBe(false);
  });

  test("recovers as soon as a poll succeeds", () => {
    expect(isStale(now - STALE_AFTER_MS, now)).toBe(true);
    expect(isStale(now, now)).toBe(false);
  });
});

describe("pollIntervalMs", () => {
  test("30s at rest, 15s once anything is wrong", () => {
    expect(pollIntervalMs("operational")).toBe(30_000);
    expect(pollIntervalMs("degraded")).toBe(15_000);
    expect(pollIntervalMs("down")).toBe(15_000);
    expect(pollIntervalMs("maintenance")).toBe(15_000);
    expect(pollIntervalMs("unknown")).toBe(15_000);
    expect(pollIntervalMs("stale")).toBe(15_000);
  });

  test("the tightened cadence still leaves room for the stale guard", () => {
    expect(pollIntervalMs("down") * 3).toBeLessThanOrEqual(STALE_AFTER_MS);
  });
});

describe("daysSince", () => {
  const now = Date.parse("2026-08-30T12:00:00Z");

  test("counts whole days", () => {
    expect(daysSince("2026-08-30T11:00:00Z", now)).toBe(0);
    expect(daysSince("2026-08-29T11:00:00Z", now)).toBe(1);
    expect(daysSince("2026-07-31T12:00:00Z", now)).toBe(30);
  });

  // A browser clock a few minutes ahead of the server must read 0, not -1.
  test("a future timestamp floors at zero rather than going negative", () => {
    expect(daysSince("2026-08-30T12:05:00Z", now)).toBe(0);
  });

  test("an unparseable timestamp yields nothing rather than NaN", () => {
    expect(daysSince("not-a-date", now)).toBeNull();
  });
});

describe("lastResolvedAt", () => {
  test("finds the newest resolution regardless of list order", () => {
    expect(
      lastResolvedAt([
        incident({ uid: "a", resolvedAt: "2026-08-01T00:00:00Z" }),
        incident({ uid: "b", resolvedAt: "2026-08-20T00:00:00Z" }),
        incident({ uid: "c", resolvedAt: "2026-08-10T00:00:00Z" }),
      ]),
    ).toBe("2026-08-20T00:00:00Z");
  });

  test("ignores incidents that are still open", () => {
    expect(
      lastResolvedAt([
        incident({ uid: "open" }),
        incident({ uid: "done", resolvedAt: "2026-08-02T00:00:00Z" }),
      ]),
    ).toBe("2026-08-02T00:00:00Z");
  });

  test("an empty or all-open history yields null", () => {
    expect(lastResolvedAt([])).toBeNull();
    expect(lastResolvedAt(undefined)).toBeNull();
    expect(lastResolvedAt([incident({})])).toBeNull();
  });
});

describe("recentResolved", () => {
  test("returns the newest N resolved, newest first", () => {
    const got = recentResolved(
      [
        incident({ uid: "a", resolvedAt: "2026-08-01T00:00:00Z" }),
        incident({ uid: "open" }),
        incident({ uid: "b", resolvedAt: "2026-08-20T00:00:00Z" }),
        incident({ uid: "c", resolvedAt: "2026-08-10T00:00:00Z" }),
        incident({ uid: "d", resolvedAt: "2026-08-05T00:00:00Z" }),
      ],
      3,
    );

    expect(got.map((i) => i.uid)).toEqual(["b", "c", "d"]);
  });

  test("copes with fewer than N", () => {
    expect(recentResolved([], 3)).toEqual([]);
    expect(recentResolved(undefined, 3)).toEqual([]);
  });
});

describe("durationParts", () => {
  test("splits into days, hours and minutes", () => {
    expect(durationParts(0)).toEqual({ days: 0, hours: 0, minutes: 0 });
    expect(durationParts(72 * 60_000)).toEqual({
      days: 0,
      hours: 1,
      minutes: 12,
    });
    expect(durationParts(26 * 60 * 60_000)).toEqual({
      days: 1,
      hours: 2,
      minutes: 0,
    });
  });

  test("a negative duration clamps to zero rather than rendering '-1m'", () => {
    expect(durationParts(-5_000)).toEqual({ days: 0, hours: 0, minutes: 0 });
  });

  // Seconds are deliberately not a unit here: a wall panel ticking every
  // second reads as a stopwatch and pulls the eye off the state.
  test("sub-minute durations read as zero minutes, not as seconds", () => {
    expect(durationParts(45_000).minutes).toBe(0);
  });
});

describe("elapsedMs", () => {
  test("measures between two timestamps", () => {
    expect(elapsedMs("2026-08-29T10:00:00Z", "2026-08-29T11:30:00Z")).toBe(
      90 * 60_000,
    );
  });

  test("accepts a numeric 'now'", () => {
    expect(
      elapsedMs("2026-08-29T10:00:00Z", Date.parse("2026-08-29T10:01:00Z")),
    ).toBe(60_000);
  });

  test("clamps a backwards clock to zero", () => {
    expect(elapsedMs("2026-08-29T11:00:00Z", "2026-08-29T10:00:00Z")).toBe(0);
  });

  test("unparseable input yields null rather than NaN on the wall", () => {
    expect(elapsedMs("nope", "2026-08-29T10:00:00Z")).toBeNull();
    expect(elapsedMs("2026-08-29T10:00:00Z", "nope")).toBeNull();
  });
});

describe("cycleWindow", () => {
  test("a list that fits is returned untouched, so no timer is needed", () => {
    expect(cycleWindow([1, 2], 2, 0)).toEqual([1, 2]);
    expect(cycleWindow([1, 2], 2, 7)).toEqual([1, 2]);
  });

  test("a longer list pages instead of shrinking", () => {
    const items = [1, 2, 3, 4, 5];

    expect(cycleWindow(items, 2, 0)).toEqual([1, 2]);
    expect(cycleWindow(items, 2, 1)).toEqual([3, 4]);
    expect(cycleWindow(items, 2, 2)).toEqual([5]);
    expect(cycleWindow(items, 2, 3)).toEqual([1, 2]);
  });

  test("every item is reachable across a full cycle", () => {
    const items = [1, 2, 3, 4, 5];
    const seen = new Set<number>();

    for (let tick = 0; tick < 3; tick += 1) {
      for (const item of cycleWindow(items, 2, tick)) seen.add(item);
    }

    expect([...seen].sort()).toEqual(items);
  });
});
