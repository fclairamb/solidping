import { describe, expect, it } from "vitest";
import enOrg from "@/locales/en/org.json";
import frOrg from "@/locales/fr/org.json";
import deOrg from "@/locales/de/org.json";
import esOrg from "@/locales/es/org.json";
import {
  formatCheckRateDemand,
  isOverCheckRateLimit,
  shouldWarnAboutCheckRate,
} from "./check-rate-limit";

describe("isOverCheckRateLimit", () => {
  it("is false when the payload is missing", () => {
    expect(isOverCheckRateLimit(undefined)).toBe(false);
    expect(isOverCheckRateLimit(null)).toBe(false);
  });

  it("treats a null limit as unlimited, never as zero", () => {
    expect(
      isOverCheckRateLimit({ demand: 5000, limit: null, skippedToday: 0 }),
    ).toBe(false);
    expect(
      isOverCheckRateLimit({ demand: 5000, skippedToday: 0 }),
    ).toBe(false);
  });

  it("is false at the cap and true above it", () => {
    expect(isOverCheckRateLimit({ demand: 10, limit: 10, skippedToday: 0 })).toBe(
      false,
    );
    expect(
      isOverCheckRateLimit({ demand: 10.5, limit: 10, skippedToday: 0 }),
    ).toBe(true);
  });

  it("recognises a zero cap as a real cap", () => {
    // 0 is falsy in JS — the check must be an explicit null/undefined test, or
    // an org suspended to 0/min would read as unlimited.
    expect(isOverCheckRateLimit({ demand: 1, limit: 0, skippedToday: 0 })).toBe(
      true,
    );
  });
});

describe("shouldWarnAboutCheckRate", () => {
  it("stays quiet for an org inside its cap that lost nothing", () => {
    expect(
      shouldWarnAboutCheckRate({ demand: 4, limit: 10, skippedToday: 0 }),
    ).toBe(false);
  });

  it("stays quiet when the server sent no figure at all", () => {
    expect(shouldWarnAboutCheckRate(undefined)).toBe(false);
  });

  it("warns predictively when demand exceeds the cap", () => {
    // Nothing has been skipped yet — the org just added checks — but it is
    // about to be, and saying so before the gaps appear is the point.
    expect(
      shouldWarnAboutCheckRate({ demand: 240, limit: 120, skippedToday: 0 }),
    ).toBe(true);
  });

  it("clears as soon as the org is back under its cap, despite today's skips", () => {
    // The org reviewed its scheduling and dropped under the cap — the exact
    // remedy the banner asks for. It must disappear right away, not linger
    // until the UTC-midnight reset of the skippedToday counter.
    expect(
      shouldWarnAboutCheckRate({ demand: 4, limit: 10, skippedToday: 613 }),
    ).toBe(false);
  });

  it("clears when the cap is lifted to unlimited, despite today's skips", () => {
    expect(
      shouldWarnAboutCheckRate({ demand: 4, limit: null, skippedToday: 12 }),
    ).toBe(false);
  });
});

describe("formatCheckRateDemand", () => {
  it("keeps whole rates whole", () => {
    expect(formatCheckRateDemand(12)).toBe("12");
    expect(formatCheckRateDemand(0)).toBe("0");
  });

  it("keeps one decimal for fractional rates", () => {
    // A check every 5 minutes contributes 0.2 executions per minute.
    expect(formatCheckRateDemand(0.2)).toBe("0.2");
    expect(formatCheckRateDemand(2.5)).toBe("2.5");
    expect(formatCheckRateDemand(1.25)).toBe("1.3");
  });
});

/*
 * Locale parity for the banner's copy. A missing key passes lint and build and
 * then renders a raw `org:checkRateLimit.title` string to a French customer —
 * exactly the kind of breakage only a test catches.
 */
const LOCALES = { en: enOrg, fr: frOrg, de: deOrg, es: esOrg } as const;

// Placeholders each string must interpolate, so a translation cannot silently
// drop the number that makes the sentence useful.
const REQUIRED_PLACEHOLDERS: Record<string, string[]> = {
  title: [],
  overLimit: ["{{demand}}", "{{limit}}"],
  skippedToday_one: ["{{count}}"],
  skippedToday_other: ["{{count}}"],
  viewUsage: [],
  upgrade: [],
};

describe("checkRateLimit locale keys", () => {
  it.each(Object.keys(LOCALES))("%s ships every key, non-empty", (locale) => {
    const bundle = (LOCALES as Record<string, { checkRateLimit?: Record<string, string> }>)[
      locale
    ];
    const block = bundle.checkRateLimit;
    expect(block, `${locale}/org.json is missing checkRateLimit`).toBeTruthy();

    for (const key of Object.keys(REQUIRED_PLACEHOLDERS)) {
      const value = block?.[key];
      expect(typeof value, `${locale}: checkRateLimit.${key}`).toBe("string");
      expect((value ?? "").trim().length, `${locale}: checkRateLimit.${key}`).toBeGreaterThan(0);
    }
  });

  it.each(Object.keys(LOCALES))("%s keeps every interpolation placeholder", (locale) => {
    const block = (LOCALES as Record<string, { checkRateLimit: Record<string, string> }>)[locale]
      .checkRateLimit;

    for (const [key, placeholders] of Object.entries(REQUIRED_PLACEHOLDERS)) {
      for (const placeholder of placeholders) {
        expect(block[key], `${locale}: checkRateLimit.${key}`).toContain(placeholder);
      }
    }
  });

  it("translates the copy rather than copying English into every locale", () => {
    const titles = Object.values(LOCALES).map(
      (bundle) => (bundle as { checkRateLimit: Record<string, string> }).checkRateLimit.title,
    );
    expect(new Set(titles).size).toBe(titles.length);
  });
});
