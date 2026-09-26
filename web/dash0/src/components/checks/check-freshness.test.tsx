/**
 * @vitest-environment jsdom
 *
 * The "No data" (stale) surfaces, spec 2026-09-25-02, rendered in every locale
 * against a one-locale i18next instance (no fallback), so a key missing from
 * fr/de/es echoes back raw and fails here instead of borrowing English.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import i18next from "i18next";
import { I18nextProvider, initReactI18next } from "react-i18next";

import checksEn from "@/locales/en/checks.json";
import checksFr from "@/locales/fr/checks.json";
import checksDe from "@/locales/de/checks.json";
import checksEs from "@/locales/es/checks.json";
import type { Check } from "@/api/hooks";
import { StatusBadge } from "@/components/shared/status-badge";
import { RegionFreshnessList, StaleSince } from "./check-freshness";

const bundles: Record<string, Record<string, unknown>> = {
  en: checksEn,
  fr: checksFr,
  de: checksDe,
  es: checksEs,
};

const EIGHT_HOURS_AGO = new Date(Date.now() - 8 * 3600_000).toISOString();
const JUST_NOW = new Date(Date.now() - 30_000).toISOString();

afterEach(cleanup);

function renderInLocale(lng: string, node: React.ReactNode) {
  const instance = i18next.createInstance();
  void instance.use(initReactI18next).init({
    lng,
    defaultNS: "checks",
    resources: { [lng]: { checks: bundles[lng] } },
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });

  render(<I18nextProvider i18n={instance}>{node}</I18nextProvider>);
}

describe("StatusBadge", () => {
  it.each(Object.keys(bundles))("labels stale with the translated 'No data' in %s", (lng) => {
    renderInLocale(lng, <StatusBadge status="stale" />);
    const badge = document.querySelector('[data-status="stale"]');
    expect(badge).not.toBeNull();
    const label = (bundles[lng].status as Record<string, string>).stale;
    expect(badge?.textContent).toBe(label);
    expect(badge?.textContent).not.toBe("stale");
    expect(badge?.querySelector("svg")).not.toBeNull(); // the clock
  });

  it.each(Object.keys(bundles))("never prints a raw wire token for created in %s", (lng) => {
    renderInLocale(lng, <StatusBadge status="created" />);
    const badge = document.querySelector('[data-status="created"]');
    expect(badge?.textContent).toBe((bundles[lng].status as Record<string, string>).created);
  });
});

describe("stale check detail", () => {
  const stale: Check = {
    uid: "c1",
    status: "stale",
    lastResultAt: EIGHT_HOURS_AGO,
    regionFreshness: [
      { region: "eu-west", lastResultAt: EIGHT_HOURS_AGO, stale: true },
      { region: "lauterbourg", lastResultAt: EIGHT_HOURS_AGO, stale: true },
    ],
  };

  it.each(Object.keys(bundles))("says 'No data since …' in %s", (lng) => {
    renderInLocale(lng, <StaleSince check={stale} />);
    const text = screen.getByTestId("check-stale-since").textContent ?? "";
    expect(text).not.toContain("detail.freshness");
    expect(text).toMatch(/\d{1,2}[:.h]\d{2}/);
  });

  it("renders nothing for a check that is not stale", () => {
    renderInLocale("en", <StaleSince check={{ uid: "c2", status: "up" }} />);
    expect(screen.queryByTestId("check-stale-since")).toBeNull();
  });

  it.each(Object.keys(bundles))(
    "names a silent region while others still report, in %s",
    (lng) => {
      const multi: Check = {
        uid: "c3",
        status: "up",
        regionFreshness: [
          { region: "eu-west", lastResultAt: JUST_NOW, stale: false },
          { region: "lauterbourg", lastResultAt: EIGHT_HOURS_AGO, stale: true },
          { region: "us-east", lastResultAt: JUST_NOW, stale: false },
        ],
      };
      renderInLocale(lng, <RegionFreshnessList check={multi} />);

      const silent = screen.getAllByTestId("region-freshness-silent");
      expect(silent).toHaveLength(1);
      expect(silent[0].textContent).toContain("lauterbourg");
      expect(silent[0].textContent).toContain("2");
      expect(silent[0].textContent).not.toContain("detail.freshness");

      const rows = screen.getAllByTestId("region-freshness-row");
      expect(rows).toHaveLength(3);
      expect(rows.filter((r) => r.getAttribute("data-stale") === "true")).toHaveLength(1);
    },
  );

  it("stays hidden when every region reports", () => {
    renderInLocale(
      "en",
      <RegionFreshnessList
        check={{
          uid: "c4",
          status: "up",
          regionFreshness: [{ region: "eu-west", lastResultAt: JUST_NOW, stale: false }],
        }}
      />,
    );
    expect(screen.queryByTestId("region-freshness")).toBeNull();
  });
});
