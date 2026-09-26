import { describe, expect, it } from "vitest";

import {
  buildDegradedPayload,
  degradedFieldsCustomized,
  buildIntervalOptions,
  canonicalPeriodHMS,
  formatPeriod,
  hmsToSeconds,
  parsePeriod,
  secondsToHMS,
  withCustomIntervalOption,
} from "./check-form";

// Long-period (>24h) support: spec "Check edit page: docs link is generic,
// domain expiration has no warning/critical days, and periods stop at 24h"
// requires the HH:MM:SS helpers to survive 3-digit hour values (168h/336h/
// 720h) without truncation.
describe("HMS helpers — periods beyond 24h", () => {
  it("hmsToSeconds parses 3-digit hour values", () => {
    expect(hmsToSeconds("168:00:00")).toBe(604800); // 1 week
    expect(hmsToSeconds("336:00:00")).toBe(1209600); // 2 weeks
    expect(hmsToSeconds("720:00:00")).toBe(2592000); // 30 days
  });

  it("secondsToHMS emits 3-digit hour values without truncation", () => {
    expect(secondsToHMS(604800)).toBe("168:00:00");
    expect(secondsToHMS(1209600)).toBe("336:00:00");
    expect(secondsToHMS(2592000)).toBe("720:00:00");
  });

  it("round-trips a 2-week period through secondsToHMS and hmsToSeconds", () => {
    const seconds = 1209600;
    expect(hmsToSeconds(secondsToHMS(seconds))).toBe(seconds);
  });

  it("formatPeriod renders a 2-week value as 336:00:00", () => {
    expect(formatPeriod(2, "weeks")).toBe("336:00:00");
  });

  it("parsePeriod recognizes a whole-week period as weeks, not days/hours", () => {
    expect(parsePeriod("336:00:00")).toEqual({ value: 2, unit: "weeks" });
    expect(parsePeriod("720:00:00")).toEqual({ value: 30, unit: "days" });
  });
});

describe("buildIntervalOptions — long-horizon entries", () => {
  it("includes 1 week / 2 weeks / 30 days when uncapped (maxSeconds = 0)", () => {
    const options = buildIntervalOptions(0, 0);
    expect(options).toContainEqual({ value: "168:00:00", label: "1 week" });
    expect(options).toContainEqual({ value: "336:00:00", label: "2 weeks" });
    expect(options).toContainEqual({ value: "720:00:00", label: "30 days" });
  });

  it("excludes long-horizon entries once maxSeconds caps below them", () => {
    // e.g. a hypothetical type capped at 24h (86400s).
    const options = buildIntervalOptions(0, 86400);
    expect(options.some((o) => o.value === "168:00:00")).toBe(false);
    expect(options[options.length - 1]).toEqual({ value: "24:00:00", label: "24 hours" });
  });

  it("respects the type's MinPeriod floor (e.g. domain's 6h)", () => {
    const options = buildIntervalOptions(6 * 3600, 0);
    expect(options[0]).toEqual({ value: "06:00:00", label: "6 hours" });
    // Long options still surface since domain has no MaxPeriod.
    expect(options).toContainEqual({ value: "720:00:00", label: "30 days" });
  });
});

// Spec 2026-08-26-05: a check whose stored period is not one of the ladder's
// steps must SHOW that period, first, marked custom. Without this the select
// renders blank and the next save silently rewrites the check's schedule.
describe("withCustomIntervalOption — honest custom periods", () => {
  const label = (hms: string) => `${hmsToSeconds(hms)}s (custom)`;

  it("prepends a 7-second period that is not on the ladder", () => {
    const options = withCustomIntervalOption(
      buildIntervalOptions(0, 0),
      "00:00:07",
      label,
    );

    expect(options[0]).toEqual({ value: "00:00:07", label: "7s (custom)" });
    expect(options.filter((o) => o.value === "00:00:07")).toHaveLength(1);
  });

  it("keeps a custom value BELOW the type's minimum visible", () => {
    // domain checks floor at 6h; a 7s period predating that floor must still
    // be shown rather than silently dropped.
    const options = withCustomIntervalOption(
      buildIntervalOptions(6 * 3600, 0),
      "00:00:07",
      label,
    );

    expect(options[0].value).toBe("00:00:07");
    expect(options[1]).toEqual({ value: "06:00:00", label: "6 hours" });
  });

  it("keeps a custom value ABOVE the type's maximum visible", () => {
    const options = withCustomIntervalOption(
      buildIntervalOptions(0, 86400),
      "48:00:00",
      label,
    );

    expect(options[0].value).toBe("48:00:00");
    expect(options.some((o) => o.value === "24:00:00")).toBe(true);
  });

  it("adds nothing when the saved period IS a ladder step", () => {
    const base = buildIntervalOptions(0, 0);
    const options = withCustomIntervalOption(base, "00:01:00", label);

    expect(options).toEqual(base);
  });

  it("adds nothing when there is no saved period (create mode)", () => {
    const base = buildIntervalOptions(0, 0);

    expect(withCustomIntervalOption(base, undefined, label)).toEqual(base);
    expect(withCustomIntervalOption(base, "", label)).toEqual(base);
    expect(withCustomIntervalOption(base, "00:00:00", label)).toEqual(base);
  });

  it("matches a ladder step written in a non-canonical form", () => {
    // "0:01:00" and "00:01:00" are the same minute: a spelling difference must
    // not mint a bogus "custom" entry.
    const base = buildIntervalOptions(0, 0);

    expect(withCustomIntervalOption(base, "0:01:00", label)).toEqual(base);
  });
});

describe("canonicalPeriodHMS", () => {
  it("normalizes a stored period so the select can match it by string", () => {
    expect(canonicalPeriodHMS("0:01:00")).toBe("00:01:00");
    expect(canonicalPeriodHMS("00:00:07")).toBe("00:00:07");
    expect(canonicalPeriodHMS("168:00:00")).toBe("168:00:00");
  });

  it("returns an empty string for a missing or zero period", () => {
    expect(canonicalPeriodHMS(undefined)).toBe("");
    expect(canonicalPeriodHMS("")).toBe("");
    expect(canonicalPeriodHMS("00:00:00")).toBe("");
  });

  it("returns an empty string for an unparseable period", () => {
    // Never render "NaN:NaN:NaN" as an option: an unexpected wire shape falls
    // back to the ladder rather than inventing a custom entry.
    expect(canonicalPeriodHMS("1m30s")).toBe("");
    expect(withCustomIntervalOption(buildIntervalOptions(0, 0), "1m30s", () => "x")).toEqual(
      buildIntervalOptions(0, 0),
    );
  });
});
// --- Degraded detection (spec 2026-09-22-03) --------------------------------
//
// The contract under test is the one that made the backend refuse a `default:`
// bun tag on every one of these columns: a typed 0 turns a rule OFF and must
// reach the server, while a blank field must be omitted so the server's own
// default stands. A form that collapsed the two would make the slow rule
// unturnable-off from the UI, which is the same class of bug twice over
// (StatusPage.AutoPublishDelaySeconds, flappingWindowSeconds: 0).

const blankDegraded = {
  degradedFailures: "",
  degradedFailuresWindow: "",
  degradedSlow: "",
  degradedSlowWindow: "",
  slowThresholdMs: "",
  degradedEnabled: true,
};

describe("buildDegradedPayload", () => {
  it("omits every blank field and still sends the enabled flag", () => {
    expect(buildDegradedPayload(blankDegraded)).toEqual({
      degradedEnabled: true,
    });
  });

  it("sends a typed 0 — the documented way to turn a rule off", () => {
    const payload = buildDegradedPayload({
      ...blankDegraded,
      degradedFailures: "0",
      degradedSlow: "0",
      slowThresholdMs: "0",
    });

    expect(payload.degradedFailures).toBe(0);
    expect(payload.degradedSlow).toBe(0);
    expect(payload.slowThresholdMs).toBe(0);
    // Still omitted, because they were still blank.
    expect(payload).not.toHaveProperty("degradedFailuresWindow");
    expect(payload).not.toHaveProperty("degradedSlowWindow");
  });

  it("carries the whole rule set when the operator fills it in", () => {
    expect(
      buildDegradedPayload({
        degradedFailures: "5",
        degradedFailuresWindow: "60",
        degradedSlow: "3",
        degradedSlowWindow: "6",
        slowThresholdMs: "900",
        degradedEnabled: true,
      }),
    ).toEqual({
      degradedFailures: 5,
      degradedFailuresWindow: 60,
      degradedSlow: 3,
      degradedSlowWindow: 6,
      slowThresholdMs: 900,
      degradedEnabled: true,
    });
  });

  it("sends degradedEnabled false rather than omitting it", () => {
    // Omitting it would make "turn this check's degraded detection off" an
    // unexpressible edit, and leaving it on is the wrong default to fall back to.
    expect(
      buildDegradedPayload({ ...blankDegraded, degradedEnabled: false }),
    ).toEqual({
      degradedEnabled: false,
    });
  });

  it("ignores an unparseable value instead of sending NaN", () => {
    const payload = buildDegradedPayload({
      ...blankDegraded,
      slowThresholdMs: "abc",
    });
    expect(payload).toEqual({ degradedEnabled: true });
  });
});

describe("degradedFieldsCustomized", () => {
  it("is false for an untouched section", () => {
    expect(degradedFieldsCustomized(blankDegraded, true)).toBe(false);
  });

  it("is true when any numeric override is set", () => {
    expect(
      degradedFieldsCustomized(
        { ...blankDegraded, slowThresholdMs: "900" },
        true,
      ),
    ).toBe(true);
  });

  it("is true when the enabled flag differs from what the check stores", () => {
    // A check that predates the feature: stored off, the operator has just
    // switched it on.
    expect(
      degradedFieldsCustomized(
        { ...blankDegraded, degradedEnabled: true },
        false,
      ),
    ).toBe(true);
    expect(
      degradedFieldsCustomized(
        { ...blankDegraded, degradedEnabled: false },
        false,
      ),
    ).toBe(false);
  });
});
