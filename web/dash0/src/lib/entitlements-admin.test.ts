import { describe, expect, it } from "vitest";
import enServer from "@/locales/en/server.json";
import frServer from "@/locales/fr/server.json";
import deServer from "@/locales/de/server.json";
import esServer from "@/locales/es/server.json";
import {
  ADMIN_LIMIT_KEYS,
  fieldsFromLimits,
  formatLimit,
  isLimitFieldValid,
  limitFieldFrom,
  limitFieldToValue,
  limitsDiff,
  limitsFromFields,
  provenanceOf,
  whiteLabelFrom,
  whiteLabelToValue,
} from "./entitlements-admin";
import type { AdminLimitKey, LimitField } from "./entitlements-admin";

describe("formatLimit", () => {
  it("renders null and undefined as the unlimited word, never blank", () => {
    // A blank cell, a dash or a 0 would each read as the OPPOSITE of what a
    // null cap means on this API.
    expect(formatLimit(null, "Unlimited")).toBe("Unlimited");
    expect(formatLimit(undefined, "Unlimited")).toBe("Unlimited");
  });

  it("renders zero as zero, not as unlimited", () => {
    // An org suspended to 0 is a real, deliberate state.
    expect(formatLimit(0, "Unlimited")).toBe("0");
  });

  it("renders a real cap", () => {
    expect(formatLimit(100, "Unlimited")).toBe("100");
  });
});

describe("limitFieldFrom / limitFieldToValue", () => {
  it("round-trips a null cap through the unlimited toggle", () => {
    const field = limitFieldFrom(null);
    expect(field).toEqual({ unlimited: true, value: "" });
    expect(limitFieldToValue(field)).toBeNull();
  });

  it("round-trips a numeric cap", () => {
    const field = limitFieldFrom(42);
    expect(field).toEqual({ unlimited: false, value: "42" });
    expect(limitFieldToValue(field)).toBe(42);
  });

  it("round-trips zero without collapsing it to unlimited", () => {
    const field = limitFieldFrom(0);
    expect(field.unlimited).toBe(false);
    expect(limitFieldToValue(field)).toBe(0);
  });
});

describe("isLimitFieldValid", () => {
  const cases: [string, LimitField, boolean][] = [
    ["unlimited needs no number", { unlimited: true, value: "" }, true],
    ["a whole number is valid", { unlimited: false, value: "12" }, true],
    ["zero is valid", { unlimited: false, value: "0" }, true],
    // An empty box must NOT be treated as unlimited: that is exactly the
    // slip that would hand an org an unbounded plan by accident.
    ["an empty box is invalid", { unlimited: false, value: "" }, false],
    ["whitespace is invalid", { unlimited: false, value: "  " }, false],
    ["a negative number is invalid", { unlimited: false, value: "-1" }, false],
    ["a decimal is invalid", { unlimited: false, value: "1.5" }, false],
    ["letters are invalid", { unlimited: false, value: "lots" }, false],
  ];

  it.each(cases)("%s", (_name, field, want) => {
    expect(isLimitFieldValid(field)).toBe(want);
  });
});

describe("limitsFromFields / fieldsFromLimits", () => {
  it("turns an all-unlimited form into an all-null payload", () => {
    const fields = fieldsFromLimits({});
    const limits = limitsFromFields(fields, "default");

    for (const key of ADMIN_LIMIT_KEYS) {
      expect(limits[key], key).toBeNull();
    }

    expect(limits.whiteLabel).toBeNull();
  });

  it("carries every modeled cap through the round trip", () => {
    const fields = fieldsFromLimits({
      maxChecks: 100,
      maxChecksPerMinute: 10,
      maxUsers: 5,
      maxDeportedAgents: 0,
      maxCustomDomains: 1,
      maxSlos: 2,
      maxSmsPerMonth: 50,
      maxCallsPerMonth: 5,
      maxWhatsappPerMonth: 25,
      whiteLabel: true,
    });

    const limits = limitsFromFields(fields, whiteLabelFrom(true));

    expect(limits).toEqual({
      maxChecks: 100,
      maxChecksPerMinute: 10,
      maxUsers: 5,
      maxDeportedAgents: 0,
      maxCustomDomains: 1,
      maxSlos: 2,
      maxSmsPerMonth: 50,
      maxCallsPerMonth: 5,
      maxWhatsappPerMonth: 25,
      whiteLabel: true,
    });
  });
});

describe("whiteLabel tri-state", () => {
  it("keeps null meaning DEFAULT, not unlimited and not false", () => {
    // The one entitlement where null is not "unbounded" — it is "use the
    // deployment default", which is a third state a checkbox cannot express.
    expect(whiteLabelFrom(null)).toBe("default");
    expect(whiteLabelFrom(undefined)).toBe("default");
    expect(whiteLabelToValue("default")).toBeNull();
  });

  it("maps the two explicit states", () => {
    expect(whiteLabelFrom(true)).toBe("allowed");
    expect(whiteLabelFrom(false)).toBe("denied");
    expect(whiteLabelToValue("allowed")).toBe(true);
    expect(whiteLabelToValue("denied")).toBe(false);
  });
});

describe("provenanceOf", () => {
  it("reads an org with no stored row as deployment defaults", () => {
    expect(provenanceOf({ source: "default" })).toEqual({ kind: "default" });
    expect(provenanceOf({ source: "self-hosted" })).toEqual({ kind: "default" });
    expect(provenanceOf({})).toEqual({ kind: "default" });
  });

  it("reports an admin override with the date it was written", () => {
    expect(
      provenanceOf({
        source: "admin",
        stored: { source: "admin", updatedAt: "2026-08-26T10:00:00Z" },
      }),
    ).toEqual({ kind: "admin", since: "2026-08-26T10:00:00Z" });
  });

  it("never invents a 'since' for an org that has no stored row", () => {
    expect(provenanceOf({ source: "admin" })).toEqual({
      kind: "admin",
      since: undefined,
    });
  });

  it("carries the billing plan name so the UI can say which plan", () => {
    expect(
      provenanceOf({
        source: "billing-service",
        displayName: "Team",
        stored: { source: "billing-service", updatedAt: "2026-08-01T00:00:00Z" },
      }),
    ).toEqual({
      kind: "billing",
      planName: "Team",
      since: "2026-08-01T00:00:00Z",
    });
  });

  it("falls back to naming an unknown source rather than guessing", () => {
    expect(provenanceOf({ source: "martian" })).toEqual({
      kind: "other",
      source: "martian",
    });
  });
});

describe("limitsDiff", () => {
  it("reports nothing when nothing moved", () => {
    expect(limitsDiff({ maxChecks: 10 }, { maxChecks: 10 })).toEqual([]);
  });

  it("treats an absent cap and an explicit null as the same thing", () => {
    // Both mean unlimited; showing "unlimited → unlimited" in a confirmation
    // dialog would train the operator to ignore it.
    expect(limitsDiff({}, { maxChecks: null })).toEqual([]);
  });

  it("reports a raised cap, in the declared field order", () => {
    const changes = limitsDiff(
      { maxChecks: 100, maxUsers: 5 },
      { maxChecks: 5000, maxUsers: 50 },
    );

    expect(changes).toEqual([
      { key: "maxChecks", from: 100, to: 5000 },
      { key: "maxUsers", from: 5, to: 50 },
    ]);
  });

  it("reports a cap being lifted to unlimited", () => {
    expect(limitsDiff({ maxChecks: 100 }, { maxChecks: null })).toEqual([
      { key: "maxChecks", from: 100, to: null },
    ]);
  });

  it("reports a white-label flip", () => {
    expect(limitsDiff({ whiteLabel: null }, { whiteLabel: true })).toEqual([
      { key: "whiteLabel", from: null, to: true },
    ]);
  });
});

/*
 * Locale parity for the editor's copy. A missing key passes lint and build and
 * then renders a raw `server:entitlements.detail.save` string to a French
 * operator — exactly the kind of breakage only a test catches.
 */
const LOCALES = { en: enServer, fr: frServer, de: deServer, es: esServer } as const;

type LocaleBundle = {
  tabs?: Record<string, string>;
  entitlements?: Record<string, unknown>;
};

// Flat keys that must exist and be non-empty, plus any placeholder each one
// has to keep so a translation cannot drop the number that makes it useful.
const REQUIRED_KEYS: Record<string, string[]> = {
  title: [],
  description: [],
  search: [],
  empty: [],
  loadError: [],
  unlimited: [],
  overLimitBadge: [],
  overLimit: ["{{demand}}", "{{limit}}"],
  saved: ["{{org}}"],
  released: ["{{org}}"],
  notReleased: ["{{org}}"],
  saveError: [],
  "provenance.default": [],
  "provenance.billing": ["{{plan}}"],
  "provenance.billingPlain": [],
  "provenance.admin": ["{{since}}"],
  "provenance.adminPlain": [],
  "provenance.other": ["{{source}}"],
  "whiteLabel.default": [],
  "whiteLabel.allowed": [],
  "whiteLabel.denied": [],
  "detail.back": [],
  "detail.limitsTitle": [],
  "detail.limitsDescription": [],
  "detail.identityTitle": [],
  "detail.identityDescription": [],
  "detail.displayName": [],
  "detail.displayEmoji": [],
  "detail.reason": [],
  "detail.reasonPlaceholder": [],
  "detail.save": [],
  "detail.release": [],
  "detail.auditTitle": [],
  "detail.auditEmpty": [],
  "detail.invalid": [],
  "detail.loadError": [],
  "detail.unlimitedToggle": [],
  "detail.defaultsNote": [],
  "confirmSave.title": ["{{org}}"],
  "confirmSave.body": ["{{org}}"],
  "confirmSave.noChanges": [],
  "confirmSave.confirm": [],
  "confirmRelease.title": ["{{org}}"],
  "confirmRelease.body": ["{{org}}"],
  "confirmRelease.confirm": [],
  "audit.suppressed": [],
  "audit.released": [],
  "audit.admin": [],
  "audit.billing": [],
  "columns.organization": [],
  "columns.source": [],
  "columns.maxChecks": [],
  "columns.maxUsers": [],
  "columns.maxChecksPerMinute": [],
  "columns.override": [],
};

function lookup(bundle: LocaleBundle, dotted: string): unknown {
  let node: unknown = bundle.entitlements;

  for (const segment of dotted.split(".")) {
    if (typeof node !== "object" || node === null) {
      return undefined;
    }

    node = (node as Record<string, unknown>)[segment];
  }

  return node;
}

describe("entitlements editor locale keys", () => {
  it.each(Object.keys(LOCALES))("%s ships every key, non-empty", (locale) => {
    const bundle = (LOCALES as Record<string, LocaleBundle>)[locale];
    expect(bundle.entitlements, `${locale}/server.json is missing entitlements`).toBeTruthy();
    expect(bundle.tabs?.entitlements, `${locale}: tabs.entitlements`).toBeTruthy();

    for (const key of Object.keys(REQUIRED_KEYS)) {
      const value = lookup(bundle, key);
      expect(typeof value, `${locale}: entitlements.${key}`).toBe("string");
      expect(
        (value as string).trim().length,
        `${locale}: entitlements.${key}`,
      ).toBeGreaterThan(0);
    }
  });

  it.each(Object.keys(LOCALES))("%s keeps every interpolation placeholder", (locale) => {
    const bundle = (LOCALES as Record<string, LocaleBundle>)[locale];

    for (const [key, placeholders] of Object.entries(REQUIRED_KEYS)) {
      for (const placeholder of placeholders) {
        expect(lookup(bundle, key), `${locale}: entitlements.${key}`).toContain(
          placeholder,
        );
      }
    }
  });

  // Every editable cap needs a label, in every locale — an unlabelled numeric
  // box in a revenue-policy form is worse than no box.
  it.each(Object.keys(LOCALES))("%s labels every editable limit", (locale) => {
    const bundle = (LOCALES as Record<string, LocaleBundle>)[locale];
    const keys: (AdminLimitKey | "whiteLabel")[] = [
      ...ADMIN_LIMIT_KEYS,
      "whiteLabel",
    ];

    for (const key of keys) {
      const label = lookup(bundle, `limits.${key}`);
      expect(typeof label, `${locale}: entitlements.limits.${key}`).toBe("string");
      expect(
        (label as string).trim().length,
        `${locale}: entitlements.limits.${key}`,
      ).toBeGreaterThan(0);
    }
  });

  it("translates the copy rather than copying English into every locale", () => {
    const titles = Object.values(LOCALES).map(
      (bundle) => (bundle as LocaleBundle).entitlements?.title,
    );
    expect(new Set(titles).size).toBe(titles.length);
  });
});
