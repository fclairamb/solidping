import { describe, expect, it } from "vitest";

import type { SloStatusRow } from "@/api/hooks";
import {
  budgetRemainingFraction,
  formatAttainment,
  formatBudgetSeconds,
  formatTarget,
  sloStateBadgeClass,
} from "./slo-format";

function row(overrides: Partial<SloStatusRow>): SloStatusRow {
  return {
    window: { start: "2026-06-01T00:00:00Z", end: "2026-07-01T00:00:00Z", label: "2026-06" },
    attainmentPct: null,
    hasData: false,
    targetPct: 99.9,
    totalChecks: 0,
    successfulChecks: 0,
    monitoredSeconds: 0,
    elapsedSeconds: 0,
    budgetTotalSeconds: 0,
    budgetConsumedSeconds: 0,
    budgetRemainingSeconds: 0,
    excludedMaintenanceSeconds: 0,
    burnRate: null,
    projectedExhaustionAt: null,
    state: "unknown",
    partial: false,
    ...overrides,
  };
}

describe("formatAttainment", () => {
  it("renders null as null so the UI can show a dash, never 100%", () => {
    expect(formatAttainment(null)).toBeNull();
    expect(formatAttainment(undefined)).toBeNull();
  });

  it("keeps three decimals so 99.9 and 99.95 stay distinguishable", () => {
    expect(formatAttainment(99.95)).toBe("99.950%");
    expect(formatAttainment(100)).toBe("100.000%");
  });
});

describe("formatTarget", () => {
  it("trims trailing zeros", () => {
    expect(formatTarget(99.9)).toBe("99.9%");
    expect(formatTarget(99)).toBe("99%");
    expect(formatTarget(99.95)).toBe("99.95%");
  });
});

describe("formatBudgetSeconds", () => {
  it("renders compact durations", () => {
    expect(formatBudgetSeconds(45)).toBe("45s");
    expect(formatBudgetSeconds(3 * 60 + 20)).toBe("3m 20s");
    expect(formatBudgetSeconds(2 * 3600 + 15 * 60)).toBe("2h 15m");
    expect(formatBudgetSeconds(3 * 86400 + 4 * 3600)).toBe("3d 4h");
  });

  it("renders an overspent budget as a negative, never as zero", () => {
    expect(formatBudgetSeconds(-750)).toBe("-12m 30s");
  });
});

describe("budgetRemainingFraction", () => {
  it("is 1 when nothing is consumed", () => {
    expect(budgetRemainingFraction(row({ budgetTotalSeconds: 100, budgetRemainingSeconds: 100 }))).toBe(1);
  });

  it("clamps a breach to 0 rather than going negative", () => {
    expect(
      budgetRemainingFraction(
        row({ budgetTotalSeconds: 100, budgetConsumedSeconds: 250, budgetRemainingSeconds: -150 }),
      ),
    ).toBe(0);
  });

  it("treats a zero budget with no consumption as full", () => {
    expect(budgetRemainingFraction(row({ budgetTotalSeconds: 0 }))).toBe(1);
    expect(budgetRemainingFraction(row({ budgetTotalSeconds: 0, budgetConsumedSeconds: 5 }))).toBe(0);
  });
});

describe("sloStateBadgeClass", () => {
  it("gives every state a distinct class, and unknown a neutral one", () => {
    const classes = new Set([
      sloStateBadgeClass("healthy"),
      sloStateBadgeClass("at_risk"),
      sloStateBadgeClass("breached"),
      sloStateBadgeClass("unknown"),
    ]);
    expect(classes.size).toBe(4);
    expect(sloStateBadgeClass("unknown")).toContain("muted");
  });
});
