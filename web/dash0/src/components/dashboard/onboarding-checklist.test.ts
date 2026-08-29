import { describe, expect, it } from "vitest";

import accountDe from "@/locales/de/account.json";
import accountEn from "@/locales/en/account.json";
import accountEs from "@/locales/es/account.json";
import accountFr from "@/locales/fr/account.json";
import dashboardDe from "@/locales/de/dashboard.json";
import dashboardEn from "@/locales/en/dashboard.json";
import dashboardEs from "@/locales/es/dashboard.json";
import dashboardFr from "@/locales/fr/dashboard.json";
import { ONBOARDING_STEP_IDS } from "@/lib/onboarding-checklist";

// The getting-started checklist (spec 2026-08-28-17) renders entirely from
// locale keys, and every one of them must exist in all four languages — a
// missing key does not fall back gracefully here, it renders the raw dotted
// key path at the user. `t()` is called with a computed path per step
// (`onboarding.steps.<id>.title`), which no static analysis catches, so this
// test walks the step ids the component actually iterates over.

const DASHBOARD_LOCALES = [
  ["en", dashboardEn],
  ["fr", dashboardFr],
  ["de", dashboardDe],
  ["es", dashboardEs],
] as const;

const ACCOUNT_LOCALES = [
  ["en", accountEn],
  ["fr", accountFr],
  ["de", accountDe],
  ["es", accountEs],
] as const;

/** Reads a dotted path out of a locale bundle, or undefined. */
function lookup(bundle: unknown, path: string): unknown {
  return path
    .split(".")
    .reduce<unknown>(
      (node, segment) =>
        node && typeof node === "object"
          ? (node as Record<string, unknown>)[segment]
          : undefined,
      bundle,
    );
}

const DASHBOARD_KEYS = [
  "onboarding.title",
  "onboarding.progress",
  "onboarding.dismiss",
  "onboarding.doneLabel",
  "onboarding.todoLabel",
  "onboarding.reenableHint",
  "onboarding.allSet.title",
  "onboarding.allSet.body",
  "onboarding.testAlert.cta",
  "onboarding.testAlert.sent",
  "onboarding.testAlert.sentDetail",
  "onboarding.testAlert.failed",
  "onboarding.testAlert.failedFallback",
  ...ONBOARDING_STEP_IDS.flatMap((id) => [
    `onboarding.steps.${id}.title`,
    `onboarding.steps.${id}.description`,
    `onboarding.steps.${id}.cta`,
  ]),
];

const ACCOUNT_KEYS = [
  "onboardingChecklist.title",
  "onboardingChecklist.subtitle",
  "onboardingChecklist.body",
  "onboardingChecklist.cta",
  "onboardingChecklist.restored",
  "onboardingChecklist.failed",
];

describe("onboarding checklist locale parity", () => {
  it.each(DASHBOARD_LOCALES)(
    "%s carries every dashboard onboarding key",
    (_locale, bundle) => {
      for (const key of DASHBOARD_KEYS) {
        const value = lookup(bundle, key);
        expect(value, `${key} is missing`).toBeTypeOf("string");
        expect(String(value).trim(), `${key} is empty`).not.toBe("");
      }
    },
  );

  it.each(ACCOUNT_LOCALES)(
    "%s carries every account re-enable key",
    (_locale, bundle) => {
      for (const key of ACCOUNT_KEYS) {
        const value = lookup(bundle, key);
        expect(value, `${key} is missing`).toBeTypeOf("string");
        expect(String(value).trim(), `${key} is empty`).not.toBe("");
      }
    },
  );

  it.each(DASHBOARD_LOCALES)(
    "%s keeps the interpolation placeholders the component passes",
    (_locale, bundle) => {
      expect(String(lookup(bundle, "onboarding.progress"))).toContain("{{done}}");
      expect(String(lookup(bundle, "onboarding.progress"))).toContain(
        "{{total}}",
      );
    },
  );

  it.each(ACCOUNT_LOCALES)(
    "%s keeps the org placeholder in the re-enable copy",
    (_locale, bundle) => {
      for (const key of [
        "onboardingChecklist.body",
        "onboardingChecklist.restored",
      ]) {
        expect(String(lookup(bundle, key)), key).toContain("{{org}}");
      }
    },
  );

  // The checklist REPLACES the old FirstResultCelebration banner outright —
  // two competing nudges is worse than either. Leaving its strings behind
  // would invite the banner back, so their absence is pinned here.
  it.each(DASHBOARD_LOCALES)(
    "%s no longer carries the retired celebration.* keys",
    (_locale, bundle) => {
      expect(lookup(bundle, "celebration")).toBeUndefined();
    },
  );
});
