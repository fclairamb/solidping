import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import {
  publicationSeverityLabel,
  publicationStateLabel,
} from "@/lib/publication-labels";

import statusUpdatesEn from "@/locales/en/statusUpdates.json";
import statusUpdatesFr from "@/locales/fr/statusUpdates.json";
import incidentsEn from "@/locales/en/incidents.json";
import incidentsFr from "@/locales/fr/incidents.json";

/**
 * A `t` backed by the real locale bundles, resolving `namespace:dotted.key` the
 * way i18next does — and, crucially, returning the KEY on a miss, exactly as
 * i18next would. That is what makes the "unknown value" tests meaningful: a
 * helper that blindly called t() would leak `statusUpdates:kinds.degraded` into
 * a badge, and a test with a forgiving fake would never notice.
 */
function tFor(bundles: Record<string, unknown>): TFunction {
  const resolve = (key: string): string => {
    const [ns, path] = key.includes(":") ? key.split(":", 2) : ["", key];
    let node: unknown = bundles[ns];
    for (const part of path.split(".")) {
      if (typeof node !== "object" || node === null || !(part in node)) return key;
      node = (node as Record<string, unknown>)[part];
    }

    return typeof node === "string" ? node : key;
  };

  return resolve as unknown as TFunction;
}

const en = tFor({ statusUpdates: statusUpdatesEn, incidents: incidentsEn });
const fr = tFor({ statusUpdates: statusUpdatesFr, incidents: incidentsFr });

describe("publicationStateLabel", () => {
  it("renders every state the API can send from the locale bundle", () => {
    // PublicationState in api/hooks.ts — all four must resolve, or a badge
    // silently starts printing an i18n key.
    expect(publicationStateLabel(en, "investigating")).toBe("Investigating");
    expect(publicationStateLabel(en, "identified")).toBe("Identified");
    expect(publicationStateLabel(en, "monitoring")).toBe("Monitoring");
    expect(publicationStateLabel(en, "resolved")).toBe("Resolved");
  });

  it("also covers the two update kinds that are not publication states", () => {
    // The publication timeline badges `update.kind` through this same helper.
    expect(publicationStateLabel(en, "maintenance")).toBe("Maintenance");
    expect(publicationStateLabel(en, "info")).toBe("Info");
  });

  it("translates — this is the whole point", () => {
    expect(publicationStateLabel(fr, "resolved")).toBe("Résolu");
    expect(publicationStateLabel(fr, "monitoring")).toBe("Surveillance");
    expect(publicationStateLabel(fr, "resolved")).not.toBe("resolved");
  });

  it("humanizes an unknown state rather than leaking an i18n key", () => {
    expect(publicationStateLabel(en, "partial_outage")).toBe("Partial Outage");
    expect(publicationStateLabel(en, "partial_outage")).not.toContain("statusUpdates:");
  });

  it("returns an empty string when there is no state", () => {
    expect(publicationStateLabel(en, undefined)).toBe("");
    expect(publicationStateLabel(en, null)).toBe("");
    expect(publicationStateLabel(en, "")).toBe("");
  });
});

describe("publicationSeverityLabel", () => {
  it("renders every severity the API can send", () => {
    expect(publicationSeverityLabel(en, "minor")).toBe("Minor");
    expect(publicationSeverityLabel(en, "major")).toBe("Major");
    expect(publicationSeverityLabel(en, "critical")).toBe("Critical");
  });

  it("translates", () => {
    expect(publicationSeverityLabel(fr, "minor")).toBe("Mineur");
    expect(publicationSeverityLabel(fr, "critical")).toBe("Critique");
  });

  it("humanizes an unknown severity rather than leaking an i18n key", () => {
    expect(publicationSeverityLabel(en, "catastrophic")).toBe("Catastrophic");
    expect(publicationSeverityLabel(en, "catastrophic")).not.toContain("incidents:");
  });

  it("returns an empty string when there is no severity", () => {
    // The badge is rendered conditionally, but a helper that returned the key
    // for an absent value would put "incidents:publications.severity" on screen
    // the first time that guard moved.
    expect(publicationSeverityLabel(en, undefined)).toBe("");
    expect(publicationSeverityLabel(en, null)).toBe("");
    expect(publicationSeverityLabel(en, "")).toBe("");
  });
});
