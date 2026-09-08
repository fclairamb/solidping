import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import { sourceLabel } from "@/lib/notifications";

import commonEn from "@/locales/en/common.json";
import commonFr from "@/locales/fr/common.json";

/**
 * A `t` backed by the real locale bundles, interpolating `{{cycle}}` the way
 * i18next does — and returning the KEY on a miss, exactly as i18next would.
 * That is what makes these assertions prove the keys EXIST: a typo would
 * render `notificationSources.escalationStep` and fail the comparison, where a
 * stub returning a canned string would pass regardless.
 */
function tFor(bundle: Record<string, unknown>): TFunction {
  const resolve = (key: string, opts?: { cycle?: number }): string => {
    let node: unknown = bundle;
    for (const part of key.split(".")) {
      if (typeof node !== "object" || node === null || !(part in node)) return key;
      node = (node as Record<string, unknown>)[part];
    }
    if (typeof node !== "string") return key;
    return opts?.cycle === undefined
      ? node
      : node.replace("{{cycle}}", String(opts.cycle));
  };

  return resolve as unknown as TFunction;
}

const t = tFor(commonEn);
const tFr = tFor(commonFr);

describe("sourceLabel", () => {
  it("labels every source the backend can record", () => {
    expect(sourceLabel(t, "check_connection")).toBe("Check connection");
    expect(sourceLabel(t, "escalation_user")).toBe("Escalation step");
    expect(sourceLabel(t, "escalation_schedule")).toBe("On-call schedule");
    expect(sourceLabel(t, "escalation_all_admins")).toBe("All admins");
    expect(sourceLabel(t, "escalation_connection")).toBe("Escalation connection");
  });

  it("appends the escalation cycle only when the row is a repeat", () => {
    // repeatIndex 0 is the first pass — "(cycle 1)" would be noise on every row.
    expect(sourceLabel(t, "escalation_user", 0)).toBe("Escalation step");
    expect(sourceLabel(t, "escalation_user", undefined)).toBe("Escalation step");
    expect(sourceLabel(t, "escalation_user", 1)).toBe("Escalation step (cycle 2)");
    expect(sourceLabel(t, "escalation_schedule", 3)).toBe("On-call schedule (cycle 4)");
  });

  it("never appends a cycle to check_connection, which cannot repeat", () => {
    expect(sourceLabel(t, "check_connection", 2)).toBe("Check connection");
  });

  it("translates — the reason this takes a t at all", () => {
    expect(sourceLabel(tFr, "check_connection")).toBe("Canal du contrôle");
    expect(sourceLabel(tFr, "escalation_user", 1)).toBe("Étape d'escalade (cycle 2)");
    expect(sourceLabel(tFr, "escalation_user")).not.toBe("Escalation step");
  });

  it("falls through to the raw token for a source it does not know", () => {
    // The backend can add a source before this frontend catches up. A raw
    // token is wrong-but-readable; a leaked i18n key is neither.
    expect(sourceLabel(t, "escalation_carrier_pigeon")).toBe("escalation_carrier_pigeon");
    expect(sourceLabel(t, "escalation_carrier_pigeon")).not.toContain("notificationSources.");
  });
});
