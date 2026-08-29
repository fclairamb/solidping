import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import integrationsEn from "@/locales/en/integrations.json";
import integrationsFr from "@/locales/fr/integrations.json";
import integrationsDe from "@/locales/de/integrations.json";
import integrationsEs from "@/locales/es/integrations.json";
import slosEn from "@/locales/en/slos.json";
import slosFr from "@/locales/fr/slos.json";
import slosDe from "@/locales/de/slos.json";
import slosEs from "@/locales/es/slos.json";
import statusPagesEn from "@/locales/en/statusPages.json";
import statusPagesFr from "@/locales/fr/statusPages.json";
import statusPagesDe from "@/locales/de/statusPages.json";
import statusPagesEs from "@/locales/es/statusPages.json";

import {
  buildEmailAlertsWandPayload,
  buildStatusPageWandPrefill,
  buildWeeklyReportWandPayload,
} from "./onboarding-wand";

// A `t` backed by a real locale bundle, resolving dotted keys the way
// i18next does — and, crucially, returning the KEY on a miss, exactly as
// i18next would (mirrors the helper in channel-labels.test.ts).
function tFor(bundle: unknown): TFunction {
  const resolve = (key: string): string => {
    const parts = key.split(".");
    let node: unknown = bundle;
    for (const part of parts) {
      if (typeof node !== "object" || node === null || !(part in node)) {
        return key;
      }
      node = (node as Record<string, unknown>)[part];
    }
    return typeof node === "string" ? node : key;
  };
  return resolve as unknown as TFunction;
}

const INTEGRATIONS_LOCALES = [
  ["en", integrationsEn],
  ["fr", integrationsFr],
  ["de", integrationsDe],
  ["es", integrationsEs],
] as const;

const SLOS_LOCALES = [
  ["en", slosEn],
  ["fr", slosFr],
  ["de", slosDe],
  ["es", slosEs],
] as const;

const STATUS_PAGES_LOCALES = [
  ["en", statusPagesEn],
  ["fr", statusPagesFr],
  ["de", statusPagesDe],
  ["es", statusPagesEs],
] as const;

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

describe("buildEmailAlertsWandPayload", () => {
  it("matches the shape seedOrgDefaults writes", () => {
    const payload = buildEmailAlertsWandPayload(
      tFor(integrationsEn),
      "alice@acme.com",
    );
    expect(payload).toEqual({
      type: "email",
      name: "Email alerts",
      enabled: true,
      isDefault: true,
      settings: { to: ["alice@acme.com"] },
    });
  });

  it.each(INTEGRATIONS_LOCALES)(
    "resolves a real integration name in %s, not the raw key",
    (_locale, bundle) => {
      const payload = buildEmailAlertsWandPayload(tFor(bundle), "alice@acme.com");
      expect(payload.name).not.toContain("wand.");
      expect(payload.name.length).toBeGreaterThan(0);
    },
  );
});

describe("buildWeeklyReportWandPayload", () => {
  it("is org-wide, includes SLOs, and carries the given timezone", () => {
    const payload = buildWeeklyReportWandPayload(
      tFor(slosEn),
      "alice@acme.com",
      "Europe/Paris",
    );
    expect(payload).toEqual({
      name: "Weekly uptime report",
      frequency: "weekly",
      timezone: "Europe/Paris",
      recipients: ["alice@acme.com"],
      checkUids: [],
      checkGroupUids: [],
      includeSlos: true,
      enabled: true,
    });
  });

  it.each(SLOS_LOCALES)(
    "resolves a real report name in %s, not the raw key",
    (_locale, bundle) => {
      const payload = buildWeeklyReportWandPayload(
        tFor(bundle),
        "alice@acme.com",
        "UTC",
      );
      expect(payload.name).not.toContain("wand.");
      expect(payload.name.length).toBeGreaterThan(0);
    },
  );
});

describe("buildStatusPageWandPrefill", () => {
  it("attaches every check in order and uses the org name", () => {
    const result = buildStatusPageWandPrefill("Acme", [
      { uid: "c1" },
      { uid: "c2" },
    ]);
    expect(result).toEqual({ name: "Acme", checkUids: ["c1", "c2"] });
  });

  it("falls back to an empty name when the org name is unknown", () => {
    expect(buildStatusPageWandPrefill(undefined, [])).toEqual({
      name: "",
      checkUids: [],
    });
  });
});

// The wand renders entirely from locale keys — a missing one does not fall
// back gracefully, it prints the raw dotted key path (or, worse, gets sent
// to the backend as a resource name). Every key the wand code paths touch
// must exist, as real text, in all four languages.
describe("wand locale parity", () => {
  const INTEGRATIONS_KEYS = [
    "wand.createEmailAlerts",
    "wand.defaultName",
    "wand.created",
    "wand.createFailed",
  ];
  const SLOS_KEYS = [
    "reports.wand.create",
    "reports.wand.defaultName",
    "reports.wand.created",
    "reports.wand.createFailed",
  ];
  const STATUS_PAGES_KEYS = [
    "wand.prefill",
    "wand.loadChecksFailed",
    "form.removeCheck",
  ];

  it.each(INTEGRATIONS_LOCALES)("integrations.json (%s)", (_locale, bundle) => {
    for (const key of INTEGRATIONS_KEYS) {
      const value = lookup(bundle, key);
      expect(typeof value, `missing key: ${key}`).toBe("string");
      expect((value as string).length).toBeGreaterThan(0);
    }
  });

  it.each(SLOS_LOCALES)("slos.json (%s)", (_locale, bundle) => {
    for (const key of SLOS_KEYS) {
      const value = lookup(bundle, key);
      expect(typeof value, `missing key: ${key}`).toBe("string");
      expect((value as string).length).toBeGreaterThan(0);
    }
  });

  it.each(STATUS_PAGES_LOCALES)("statusPages.json (%s)", (_locale, bundle) => {
    for (const key of STATUS_PAGES_KEYS) {
      const value = lookup(bundle, key);
      expect(typeof value, `missing key: ${key}`).toBe("string");
      expect((value as string).length).toBeGreaterThan(0);
    }
  });
});
