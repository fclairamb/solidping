import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import { describePeriod, formatDuration } from "@/lib/period-estimate";

// fakeT mimics just enough of the i18next checks-namespace translator to test
// the helper without booting i18n: it interpolates {{...}} placeholders and
// picks the `_one` / `_other` plural form from the English rule (count === 1).
// The strings mirror locales/en/checks.json so the assertions read like the UI.
const STRINGS: Record<string, string> = {
  "form.periodEstimate": "= {{duration}}",
  "form.periodEstimateChecks_one": "· ≈ {{count}} check at the {{interval}} interval",
  "form.periodEstimateChecks_other": "· ≈ {{count}} checks at the {{interval}} interval",
  "form.periodImmediateOpen": "Opens an incident immediately on the first failure.",
  "form.periodImmediateResolve":
    "Resolves the incident immediately on the first success.",
};

const fakeT = ((key: string, opts?: Record<string, unknown>): string => {
  let resolved = key;
  if (opts && typeof opts.count === "number") {
    const suffix = opts.count === 1 ? "_one" : "_other";
    if (STRINGS[`${key}${suffix}`] !== undefined) resolved = `${key}${suffix}`;
  }
  let out = STRINGS[resolved] ?? resolved;
  if (opts) {
    for (const [k, v] of Object.entries(opts)) {
      out = out.replace(new RegExp(`{{\\s*${k}\\s*}}`, "g"), String(v));
    }
  }
  return out;
}) as unknown as TFunction;

describe("formatDuration", () => {
  it("renders minutes", () => {
    expect(formatDuration(120)).toBe("2 min");
  });

  it("renders minutes and seconds", () => {
    expect(formatDuration(90)).toBe("1 min 30 s");
  });

  it("renders whole hours", () => {
    expect(formatDuration(3600)).toBe("1 h");
  });

  it("renders hours, minutes and seconds", () => {
    expect(formatDuration(3661)).toBe("1 h 1 min 1 s");
  });

  it("renders bare seconds", () => {
    expect(formatDuration(5)).toBe("5 s");
  });
});

describe("describePeriod", () => {
  it("appends an approximate probe count for active checks (N checks)", () => {
    // 120s window at a 60s interval -> 2 min, ~2 checks (plural).
    expect(describePeriod(120, 60, "confirmation", fakeT)).toBe(
      "= 2 min · ≈ 2 checks at the 1 min interval",
    );
  });

  it("uses the singular plural form when the count is 1", () => {
    // 3600s window at a 3600s interval -> 1 h, exactly 1 check (singular).
    expect(describePeriod(3600, 3600, "recovery", fakeT)).toBe(
      "= 1 h · ≈ 1 check at the 1 h interval",
    );
  });

  it("floors the count to at least 1 when the window is shorter than the interval", () => {
    // 120s window at a 3600s interval rounds to 0 -> floored to 1 check.
    expect(describePeriod(120, 3600, "confirmation", fakeT)).toBe(
      "= 2 min · ≈ 1 check at the 1 h interval",
    );
  });

  it("counts many probes at a short interval", () => {
    // 120s window at a 5s interval -> 24 checks (matches spec example).
    expect(describePeriod(120, 5, "confirmation", fakeT)).toBe(
      "= 2 min · ≈ 24 checks at the 5 s interval",
    );
  });

  it("shows the duration only for passive checks (intervalSeconds <= 0)", () => {
    expect(describePeriod(120, 0, "confirmation", fakeT)).toBe("= 2 min");
    expect(describePeriod(300, -1, "recovery", fakeT)).toBe("= 5 min");
  });

  it("returns the immediate-open copy for a confirmation window of 0", () => {
    expect(describePeriod(0, 60, "confirmation", fakeT)).toBe(
      "Opens an incident immediately on the first failure.",
    );
    // Independent of interval / check type.
    expect(describePeriod(0, 0, "confirmation", fakeT)).toBe(
      "Opens an incident immediately on the first failure.",
    );
  });

  it("returns the immediate-resolve copy for a recovery window of 0", () => {
    expect(describePeriod(0, 60, "recovery", fakeT)).toBe(
      "Resolves the incident immediately on the first success.",
    );
  });

  it("returns an empty string for a negative or non-finite window", () => {
    expect(describePeriod(-5, 60, "confirmation", fakeT)).toBe("");
    expect(describePeriod(NaN, 60, "confirmation", fakeT)).toBe("");
  });
});
