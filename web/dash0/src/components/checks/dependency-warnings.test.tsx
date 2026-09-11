/**
 * @vitest-environment jsdom
 *
 * Locale parity for DependencyWarningHint — the inline confirmation-margin
 * warning glyph (spec 2026-08-31-06, moved off the stacked-banner form by
 * spec 2026-09-10-02).
 *
 * This renders the REAL component against a throwaway i18next instance
 * carrying only ONE locale's bundle at a time, rather than hand-listing the
 * keys it happens to call today. A hand-maintained key list can silently
 * drift from what the component actually renders (a key gets renamed or
 * added in the component and the list is never updated); rendering the
 * component means whatever it asks i18next for is what gets exercised here.
 *
 * Each locale gets its OWN i18next instance with no fallbackLng and no other
 * language's resources loaded, so a key missing from e.g. `es/dependencies`
 * alone — even though `en/dependencies` has it — echoes back as the raw key
 * (i18next's documented behaviour with nothing to fall back to) instead of
 * silently borrowing the English copy the way the app's shared `@/i18n`
 * singleton (fallbackLng: "en") would. That's what lets the assertions below
 * actually fail per locale.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import i18next from "i18next";
import { I18nextProvider, initReactI18next } from "react-i18next";

import depsEn from "@/locales/en/dependencies.json";
import depsFr from "@/locales/fr/dependencies.json";
import depsDe from "@/locales/de/dependencies.json";
import depsEs from "@/locales/es/dependencies.json";
import type { DependencyWarning } from "@/api/hooks";
import { DependencyWarningHint } from "./dependency-warnings";

const bundles: Record<string, Record<string, unknown>> = {
  en: depsEn,
  fr: depsFr,
  de: depsDe,
  es: depsEs,
};

const WARNING: DependencyWarning = {
  code: "CONFIRMATION_MARGIN_TOO_SHORT",
  dependencyUid: "edge-1",
  parentCheck: { uid: "p1", slug: "rabbitmq-aws", name: "rabbitmq-aws" },
  childConfirmationSeconds: 120,
  recommendedConfirmationSeconds: 195,
  message: "",
};

afterEach(cleanup);

function renderInLocale(lng: string) {
  const instance = i18next.createInstance();
  // Synchronous init (no backend, resources passed directly), so the
  // component can render immediately without waiting on a promise.
  void instance.use(initReactI18next).init({
    lng,
    resources: { [lng]: { dependencies: bundles[lng] } },
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });

  render(
    <I18nextProvider i18n={instance}>
      <DependencyWarningHint warning={WARNING} />
    </I18nextProvider>,
  );
}

describe("DependencyWarningHint locale coverage", () => {
  it.each(Object.keys(bundles))(
    "renders real, interpolated copy in %s — not a raw i18next key",
    (lng) => {
      renderInLocale(lng);

      const trigger = screen.getByTestId("dependency-warning-hint");
      const ariaLabel = trigger.getAttribute("aria-label") ?? "";

      // With no fallbackLng and only this one locale's bundle loaded, a
      // missing key makes i18next echo the bare (ns-stripped) key back —
      // e.g. "warnings.confirmationMargin.title" — rather than interpolating
      // real copy. The positive assertions below (the parent name and both
      // durations actually present) are what catches that: raw key text
      // can never coincidentally contain them. Verified by temporarily
      // pointing the component at a typo'd key and confirming every locale
      // failed here before revert.
      expect(ariaLabel).not.toBe("warnings.confirmationMargin.title");
      expect(ariaLabel.trim().length).toBeGreaterThan(0);
      expect(ariaLabel).toContain("rabbitmq-aws");

      fireEvent.click(trigger);

      // The Popover content (title + body) is what the tap/click reveals —
      // rendered via a Portal, so read it off the document rather than a
      // container scoped to the trigger.
      const popoverText = document.body.textContent ?? "";
      expect(popoverText).not.toContain("warnings.confirmationMargin.title");
      expect(popoverText).not.toContain("warnings.confirmationMargin.body");
      expect(popoverText).toContain("rabbitmq-aws");
      expect(popoverText).toContain("120");
      expect(popoverText).toContain("195");
    },
  );
});
