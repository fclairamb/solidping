import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import {
  calculateReopenCooldownSeconds,
  describeReopenCooldown,
  REOPEN_COOLDOWN_MAX_SECONDS,
  REOPEN_COOLDOWN_MIN_SECONDS,
} from "@/lib/reopen-cooldown";

// fakeT mimics just enough of the i18next checks-namespace translator to test
// the helper without booting i18n. Strings mirror locales/en/checks.json —
// see lib/period-estimate.test.ts for the same idiom.
const STRINGS: Record<string, string> = {
  "form.periodEstimate": "= {{duration}}",
  "form.reopenCooldownOff": "off — always opens a new incident",
  "form.reopenCooldownCapped": "= {{duration}} (capped, from {{raw}})",
  "form.reopenCooldownFloored": "= {{duration}} (floor, from {{raw}})",
};

const fakeT = ((key: string, opts?: Record<string, unknown>): string => {
  let out = STRINGS[key] ?? key;
  if (opts) {
    for (const [k, v] of Object.entries(opts)) {
      out = out.replace(new RegExp(`{{\\s*${k}\\s*}}`, "g"), String(v));
    }
  }
  return out;
}) as unknown as TFunction;

describe("calculateReopenCooldownSeconds", () => {
  it("returns 0 (disabled) for a multiplier of 0", () => {
    expect(calculateReopenCooldownSeconds(0, 60)).toEqual({
      seconds: 0,
      rawSeconds: 0,
      clamp: "none",
    });
  });

  it("returns 0 for a negative or non-finite multiplier", () => {
    expect(calculateReopenCooldownSeconds(-1, 60).seconds).toBe(0);
    expect(calculateReopenCooldownSeconds(NaN, 60).seconds).toBe(0);
  });

  it("computes the plain multiplier × period when inside the clamp range", () => {
    // Default multiplier 5 on a 60s check -> 300s = 5 min, no clamp.
    expect(calculateReopenCooldownSeconds(5, 60)).toEqual({
      seconds: 300,
      rawSeconds: 300,
      clamp: "none",
    });
  });

  it("caps at 30 minutes when the raw window exceeds it", () => {
    // Spec's own example: multiplier 60 on a 1-min check -> 3600s raw,
    // clamped to 1800s (30 min).
    expect(calculateReopenCooldownSeconds(60, 60)).toEqual({
      seconds: REOPEN_COOLDOWN_MAX_SECONDS,
      rawSeconds: 3600,
      clamp: "capped",
    });
  });

  it("floors at 2 minutes when the raw window is below it", () => {
    // multiplier 1 on a 10s check -> 10s raw, floored to 120s (2 min).
    expect(calculateReopenCooldownSeconds(1, 10)).toEqual({
      seconds: REOPEN_COOLDOWN_MIN_SECONDS,
      rawSeconds: 10,
      clamp: "floored",
    });
  });

  it("is exactly at the boundary is not clamped (min)", () => {
    expect(calculateReopenCooldownSeconds(2, 60)).toEqual({
      seconds: 120,
      rawSeconds: 120,
      clamp: "none",
    });
  });

  it("is exactly at the boundary is not clamped (max)", () => {
    expect(calculateReopenCooldownSeconds(30, 60)).toEqual({
      seconds: 1800,
      rawSeconds: 1800,
      clamp: "none",
    });
  });
});

describe("describeReopenCooldown", () => {
  it("normal: renders the plain computed window", () => {
    expect(describeReopenCooldown(5, 60, fakeT)).toBe("= 5 min");
  });

  it("capped: shows both the clamped and the raw window", () => {
    expect(describeReopenCooldown(60, 60, fakeT)).toBe(
      "= 30 min (capped, from 1 h)",
    );
  });

  it("floored: shows both the clamped and the raw window", () => {
    expect(describeReopenCooldown(1, 10, fakeT)).toBe(
      "= 2 min (floor, from 10 s)",
    );
  });

  it("0: reopening disabled entirely", () => {
    expect(describeReopenCooldown(0, 60, fakeT)).toBe(
      "off — always opens a new incident",
    );
  });
});
