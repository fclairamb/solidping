import { beforeAll, describe, expect, it } from "vitest";
import i18next, { type TFunction } from "i18next";

import { formatIssuesSubtitle } from "@/lib/issues-banner";

import dashboardEn from "@/locales/en/dashboard.json";
import dashboardFr from "@/locales/fr/dashboard.json";
import dashboardDe from "@/locales/de/dashboard.json";
import dashboardEs from "@/locales/es/dashboard.json";

// A REAL i18next instance (not a hand-rolled resolver) backed by the actual
// dashboard.json bundle, because what this pins is plural-form SELECTION
// (`_one` vs `_other`) — the exact mechanism that produced the bug (spec
// 2026-08-28-10): a single `count` selector drove both fragments, so a
// recovered check with an open incident rendered "No active incidents".
// A hand-rolled dotted-key resolver wouldn't reproduce i18next's plural
// pluralization rules at all.
let t: TFunction;

beforeAll(async () => {
  const instance = i18next.createInstance();
  await instance.init({
    lng: "en",
    resources: { en: { dashboard: dashboardEn } },
    interpolation: { escapeValue: false },
  });
  t = instance.getFixedT("en", "dashboard");
});

describe("formatIssuesSubtitle", () => {
  it("joins both fragments, each pluralized on its own count", () => {
    expect(formatIssuesSubtitle(t, 2, 1)).toBe("2 checks down, 1 active incident");
  });

  it("pluralizes the down fragment independently of the incidents fragment", () => {
    // This is the second symptom from the spec: a single shared `count`
    // meant a down=1 could never coexist with a correctly pluralized
    // incidents=2 fragment. Each must select its own plural form.
    expect(formatIssuesSubtitle(t, 1, 2)).toBe("1 check down, 2 active incidents");
  });

  it("omits the incidents fragment when incidents = 0", () => {
    expect(formatIssuesSubtitle(t, 2, 0)).toBe("2 checks down");
  });

  it("omits the down fragment when down = 0, and never says 'No active incidents'", () => {
    // The exact regression: check recovered (hardDownCount = 0) but the
    // incident is still open (incidentsCount > 0) — the state that keeps the
    // red banner alive via `hardDownCount > 0 || incidentsCount > 0`.
    const result = formatIssuesSubtitle(t, 0, 1);
    expect(result).toBe("1 active incident");
    expect(result).not.toMatch(/no active incident/i);
  });

  it("pluralizes a single down check on its own", () => {
    expect(formatIssuesSubtitle(t, 1, 0)).toBe("1 check down");
  });
});

describe("formatIssuesSubtitle locale parity", () => {
  it.each([
    ["fr", dashboardFr],
    ["de", dashboardDe],
    ["es", dashboardEs],
  ] as const)(
    "%s carries issuesSubDown_one/_other and issuesSubIncidents_one/_other, and no leftover issuesSub_*",
    (_locale, dashboard) => {
      const banner = dashboard.banner as Record<string, unknown>;
      expect(typeof banner.issuesSubDown_one).toBe("string");
      expect(typeof banner.issuesSubDown_other).toBe("string");
      expect(typeof banner.issuesSubIncidents_one).toBe("string");
      expect(typeof banner.issuesSubIncidents_other).toBe("string");
      expect(banner.issuesSub_zero).toBeUndefined();
      expect(banner.issuesSub_one).toBeUndefined();
      expect(banner.issuesSub_other).toBeUndefined();
    },
  );

  it.each([
    ["fr", dashboardFr],
    ["de", dashboardDe],
    ["es", dashboardEs],
  ] as const)("%s composes a non-English subtitle end to end", async (locale, dashboard) => {
    const instance = i18next.createInstance();
    await instance.init({
      lng: locale,
      resources: { [locale]: { dashboard } },
      interpolation: { escapeValue: false },
    });
    const localeT = instance.getFixedT(locale, "dashboard");

    const result = formatIssuesSubtitle(localeT, 2, 1);
    expect(result).toContain("2");
    expect(result).toContain("1");
    expect(result).not.toBe("");
  });
});
