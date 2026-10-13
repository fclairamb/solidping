/**
 * @vitest-environment jsdom
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import i18next from "i18next";
import { I18nextProvider, initReactI18next } from "react-i18next";

import checksEn from "@/locales/en/checks.json";
import checksFr from "@/locales/fr/checks.json";
import checksDe from "@/locales/de/checks.json";
import checksEs from "@/locales/es/checks.json";
import { extractHttpTimingPhases } from "@/lib/http-timing";
import { HttpTimingCard } from "./http-timing-card";

const bundles: Record<string, Record<string, unknown>> = {
  en: checksEn,
  fr: checksFr,
  de: checksDe,
  es: checksEs,
};

function renderCard(lng: string, metrics: Record<string, unknown>) {
  const instance = i18next.createInstance();
  void instance.use(initReactI18next).init({
    lng,
    resources: { [lng]: { checks: bundles[lng] } },
    ns: ["checks"],
    defaultNS: "checks",
    initImmediate: false,
  });
  return render(
    <I18nextProvider i18n={instance}>
      <HttpTimingCard metrics={metrics} />
    </I18nextProvider>,
  );
}

afterEach(cleanup);

describe("HttpTimingCard", () => {
  it("draws the present phases in display order", () => {
    renderCard("en", {
      download_time_ms: 5,
      ttfb_time_ms: 40,
      tls_time_ms: 12,
      dns_time_ms: 3,
      connect_time_ms: 8,
      redirect_time_ms: 20,
    });
    const rows = screen
      .getAllByTestId(/^http-timing-phase-/)
      .map((el) => el.getAttribute("data-testid"));
    expect(rows).toEqual([
      "http-timing-phase-redirect",
      "http-timing-phase-dns",
      "http-timing-phase-connect",
      "http-timing-phase-tls",
      "http-timing-phase-ttfb",
      "http-timing-phase-download",
    ]);
  });

  it("does not draw missing phases", () => {
    renderCard("en", { connect_time_ms: 8, ttfb_time_ms: 40, other: 1 });
    expect(screen.getByTestId("http-timing-card")).toBeTruthy();
    expect(screen.queryByTestId("http-timing-phase-tls")).toBeNull();
    expect(screen.queryByTestId("http-timing-phase-dns")).toBeNull();
    expect(screen.getByTestId("http-timing-phase-connect")).toBeTruthy();
  });

  it("renders nothing without any phase", () => {
    renderCard("en", { something: 3 });
    expect(screen.queryByTestId("http-timing-card")).toBeNull();
  });

  it("translates the phase labels in every locale", () => {
    for (const lng of ["en", "fr", "de", "es"]) {
      renderCard(lng, { dns_time_ms: 1 });
      const text =
        screen.getByTestId("http-timing-phase-dns").textContent ?? "";
      expect(text).not.toContain("timing.phase");
      cleanup();
    }
  });

  it("extracts only finite non-negative numbers", () => {
    expect(
      extractHttpTimingPhases({
        dns_time_ms: "x",
        tls_time_ms: -1,
        ttfb_time_ms: 2,
      }),
    ).toEqual([{ id: "ttfb", key: "ttfb_time_ms", ms: 2 }]);
  });
});
