import { describe, expect, it } from "vitest";

import dashboardEn from "@/locales/en/dashboard.json";
import dashboardFr from "@/locales/fr/dashboard.json";
import dashboardDe from "@/locales/de/dashboard.json";
import dashboardEs from "@/locales/es/dashboard.json";

// Pins the deliberate decision (spec 2026-08-24-07) that the onboarding
// quick-start "ssl" chip keeps saying "SSL" even though every other surface
// renamed the check type's label to "TLS" (spec 2026-08-24-04). This is
// NOT the same guard as check-type-identity.test.ts's `it.each` pinning
// check-form.tsx to CHECK_TYPE_IDENTITY — extending that guard to this
// surface would force `icmp` to become "ICMP" here too and destroy the
// deliberate beginner-facing copy ("Ping"). This test only asserts what was
// actually decided for `ssl`, so a future accidental rename in one of these
// four locale files (or a drive-by "fix" to match CHECK_TYPE_IDENTITY) is
// caught without re-imposing that broader constraint.
describe("onboarding quick-start ssl label", () => {
  it.each([
    ["en", dashboardEn],
    ["fr", dashboardFr],
    ["de", dashboardDe],
    ["es", dashboardEs],
  ] as const)("%s keeps the chip label 'SSL'", (_locale, dashboard) => {
    expect(dashboard.welcome.quick.ssl).toBe("SSL");
  });

  it.each([
    ["en", dashboardEn],
    ["fr", dashboardFr],
    ["de", dashboardDe],
    ["es", dashboardEs],
  ] as const)("%s keeps the input label mentioning SSL, not TLS", (_locale, dashboard) => {
    expect(dashboard.welcome.quickLabel.ssl).toMatch(/SSL/);
    expect(dashboard.welcome.quickLabel.ssl).not.toMatch(/TLS/);
  });
});
