import { describe, expect, it } from "vitest";

import enChecks from "@/locales/en/checks.json";
import frChecks from "@/locales/fr/checks.json";
import deChecks from "@/locales/de/checks.json";
import esChecks from "@/locales/es/checks.json";
import type { Check, CheckTypeInfo } from "@/api/hooks";
import { buildIntervalOptions, hmsToSeconds } from "@/components/shared/check-form";
import {
  PERIOD_STEP_SECONDS,
  allowedStepsFor,
  buildSchedulingRows,
  contributionPerMinute,
  describePeriod,
  formatRate,
  isPassiveCheckType,
  isRowDirty,
  nextLongerStep,
  periodOptionsFor,
  periodToHMS,
  proposeRebalance,
  rowContribution,
  savedTotalDemand,
  totalDemand,
  type SchedulingRow,
} from "./check-scheduling";

function row(overrides: Partial<SchedulingRow> = {}): SchedulingRow {
  const base: SchedulingRow = {
    uid: "u1",
    name: "check",
    type: "http",
    regions: [],
    currentPeriodSeconds: 60,
    currentEnabled: true,
    periodSeconds: 60,
    enabled: true,
    minPeriodSeconds: 5,
    maxPeriodSeconds: 0,
  };

  return { ...base, ...overrides };
}

describe("PERIOD_STEP_SECONDS", () => {
  it("is exactly the ladder the check form offers", () => {
    // The page's arithmetic and the form's dropdown must snap to the same
    // steps. If the form gains or drops one, this fails rather than letting
    // the two surfaces drift into offering different periods.
    const formSteps = buildIntervalOptions(0, 0).map((opt) =>
      hmsToSeconds(opt.value),
    );

    expect([...PERIOD_STEP_SECONDS]).toEqual(formSteps);
  });

  it("is strictly ascending, which the stretch logic relies on", () => {
    for (let i = 1; i < PERIOD_STEP_SECONDS.length; i++) {
      expect(PERIOD_STEP_SECONDS[i]).toBeGreaterThan(
        PERIOD_STEP_SECONDS[i - 1],
      );
    }
  });
});

describe("isPassiveCheckType", () => {
  it("matches the server's IsPassive() exactly", () => {
    expect(isPassiveCheckType("heartbeat")).toBe(true);
    expect(isPassiveCheckType("email")).toBe(true);
    // Positive control: every other type is active and DOES cost budget.
    for (const type of ["http", "tcp", "icmp", "dns", "ssl", "smtp", "sleep"]) {
      expect(isPassiveCheckType(type)).toBe(false);
    }
    expect(isPassiveCheckType(undefined)).toBe(false);
    expect(isPassiveCheckType(null)).toBe(false);
  });
});

describe("contributionPerMinute", () => {
  it("matches the server formula: max(1, regions) x 60 / period", () => {
    expect(contributionPerMinute(1, 60)).toBe(1);
    expect(contributionPerMinute(1, 30)).toBe(2);
    expect(contributionPerMinute(1, 300)).toBeCloseTo(0.2, 10);
  });

  it("multiplies by the region count — each region runs the FULL period", () => {
    // The trap this guards: dividing by regions (as if they shared the work)
    // would UNDER-report demand and quietly disagree with the server's cap.
    expect(contributionPerMinute(3, 60)).toBe(3);
    expect(contributionPerMinute(4, 30)).toBe(8);
  });

  it("treats zero regions as one", () => {
    expect(contributionPerMinute(0, 60)).toBe(1);
  });

  it("returns 0 for a non-positive or non-finite period", () => {
    expect(contributionPerMinute(2, 0)).toBe(0);
    expect(contributionPerMinute(2, -60)).toBe(0);
    expect(contributionPerMinute(2, Number.NaN)).toBe(0);
  });
});

describe("rowContribution / totals", () => {
  it("counts nothing for a disabled row", () => {
    expect(rowContribution(row({ enabled: false, periodSeconds: 10 }))).toBe(0);
  });

  it("separates the saved total from the draft total", () => {
    const rows = [
      row({ uid: "a", currentPeriodSeconds: 60, periodSeconds: 300 }),
      row({ uid: "b", currentPeriodSeconds: 60, periodSeconds: 60 }),
    ];

    expect(savedTotalDemand(rows)).toBe(2);
    expect(totalDemand(rows)).toBeCloseTo(1.2, 10);
  });

  it("drops a row from the draft total when it is switched off", () => {
    const rows = [row({ uid: "a", enabled: false })];

    expect(savedTotalDemand(rows)).toBe(1);
    expect(totalDemand(rows)).toBe(0);
  });
});

describe("isRowDirty", () => {
  it("is false on an untouched row and true on either edit", () => {
    expect(isRowDirty(row())).toBe(false);
    expect(isRowDirty(row({ periodSeconds: 300 }))).toBe(true);
    expect(isRowDirty(row({ enabled: false }))).toBe(true);
  });
});

function check(overrides: Partial<Check> = {}): Check {
  return {
    uid: "c1",
    name: "Check",
    type: "http",
    period: "00:01:00",
    enabled: true,
    ...overrides,
  } as Check;
}

const TYPE_INFO = new Map<string, CheckTypeInfo>([
  [
    "http",
    {
      type: "http",
      description: "",
      labels: [],
      enabled: true,
      minPeriodSeconds: 10,
      maxPeriodSeconds: 0,
    },
  ],
  [
    "domain",
    {
      type: "domain",
      description: "",
      labels: [],
      enabled: true,
      minPeriodSeconds: 3600,
      maxPeriodSeconds: 86400,
    },
  ],
]);

describe("buildSchedulingRows", () => {
  it("excludes passive checks and reports how many it hid", () => {
    const { rows, passiveCount } = buildSchedulingRows(
      [
        check({ uid: "a", type: "http" }),
        check({ uid: "b", type: "heartbeat" }),
        check({ uid: "c", type: "email" }),
      ],
      TYPE_INFO,
    );

    expect(rows.map((r) => r.uid)).toEqual(["a"]);
    expect(passiveCount).toBe(2);
  });

  it("never lets a passive check reach the demand total", () => {
    // Negative proof: a 5-second heartbeat across 4 regions would be 48/min if
    // it were counted, and the server would still admit it — the page must not
    // invite the user to slow it down.
    const { rows } = buildSchedulingRows(
      [
        check({
          uid: "hb",
          type: "heartbeat",
          period: "00:00:05",
          regions: ["a", "b", "c", "d"],
        }),
      ],
      TYPE_INFO,
    );

    expect(rows).toHaveLength(0);
    expect(totalDemand(rows)).toBe(0);
  });

  it("excludes internal checks, like the server's demand figure does", () => {
    const { rows } = buildSchedulingRows(
      [check({ uid: "a" }), check({ uid: "i", internal: true })],
      TYPE_INFO,
    );

    expect(rows.map((r) => r.uid)).toEqual(["a"]);
  });

  it("keeps disabled checks so re-enabling one is possible", () => {
    const { rows } = buildSchedulingRows(
      [check({ uid: "off", enabled: false })],
      TYPE_INFO,
    );

    expect(rows).toHaveLength(1);
    expect(rows[0].enabled).toBe(false);
    expect(rowContribution(rows[0])).toBe(0);
  });

  it("sorts heaviest first and sinks disabled rows, tie-breaking on uid", () => {
    const { rows } = buildSchedulingRows(
      [
        check({ uid: "slow", period: "00:05:00" }),
        check({ uid: "off", period: "00:00:10", enabled: false }),
        check({ uid: "fast", period: "00:00:10" }),
        check({ uid: "aaa", period: "00:05:00" }),
      ],
      TYPE_INFO,
    );

    expect(rows.map((r) => r.uid)).toEqual(["fast", "aaa", "slow", "off"]);
  });

  it("carries per-type period constraints onto the row", () => {
    const { rows } = buildSchedulingRows(
      [check({ uid: "d", type: "domain", period: "06:00:00" })],
      TYPE_INFO,
    );

    expect(rows[0].minPeriodSeconds).toBe(3600);
    expect(rows[0].maxPeriodSeconds).toBe(86400);
  });

  it("counts regions from the check, defaulting a region-less check to 1", () => {
    const { rows } = buildSchedulingRows(
      [check({ uid: "multi", regions: ["eu", "us", "ap"] })],
      TYPE_INFO,
    );

    expect(rowContribution(rows[0])).toBe(3);
  });
});

describe("allowedStepsFor / periodOptionsFor", () => {
  it("clamps to the type's min and max", () => {
    const steps = allowedStepsFor({
      minPeriodSeconds: 3600,
      maxPeriodSeconds: 86400,
    });

    expect(steps).toEqual([3600, 21600, 43200, 86400]);
  });

  it("treats a zero max as unbounded", () => {
    const steps = allowedStepsFor({
      minPeriodSeconds: 43200,
      maxPeriodSeconds: 0,
    });

    expect(steps).toEqual([43200, 86400, 604800, 1209600, 2592000]);
  });

  it("offers only the ladder when the current period IS a step", () => {
    const options = periodOptionsFor(
      row({ periodSeconds: 60, minPeriodSeconds: 10 }),
    );

    expect(options.some((o) => o.custom)).toBe(false);
    expect(options.map((o) => o.seconds)).toContain(60);
  });

  it("prepends a custom entry for a period that is not a step", () => {
    // A check imported or created through the API can hold any period; a
    // select that omitted it would display the wrong value and silently
    // rewrite it on the first save.
    const options = periodOptionsFor(
      row({ periodSeconds: 45, minPeriodSeconds: 10 }),
    );

    expect(options[0]).toEqual({ seconds: 45, custom: true });
    expect(options.filter((o) => o.custom)).toHaveLength(1);
  });

  it("keeps a custom period that is outside the type's constraints", () => {
    const options = periodOptionsFor(
      row({ periodSeconds: 90, minPeriodSeconds: 3600, maxPeriodSeconds: 86400 }),
    );

    expect(options[0]).toEqual({ seconds: 90, custom: true });
    expect(options.slice(1).map((o) => o.seconds)).toEqual([
      3600, 21600, 43200, 86400,
    ]);
  });
});

describe("nextLongerStep", () => {
  it("jumps to the next ladder step above a non-standard period", () => {
    expect(nextLongerStep(row({ periodSeconds: 45 }))).toBe(60);
  });

  it("is null at the longest allowed step", () => {
    expect(
      nextLongerStep(row({ periodSeconds: 86400, maxPeriodSeconds: 86400 })),
    ).toBeNull();
  });
});

describe("proposeRebalance", () => {
  const constrained = (uid: string, periodSeconds: number, regions = 1) =>
    row({
      uid,
      regions: Array.from({ length: regions }, (_, i) => `r${i}`),
      currentPeriodSeconds: periodSeconds,
      periodSeconds,
      minPeriodSeconds: 5,
      maxPeriodSeconds: 0,
    });

  it("proposes nothing when already at or under the cap", () => {
    const rows = [constrained("a", 60), constrained("b", 60)];
    const proposal = proposeRebalance(rows, 2);

    expect(proposal.proposals.size).toBe(0);
    expect(proposal.reachedLimit).toBe(true);
    expect(proposal.totalAfter).toBe(2);
  });

  it("proposes nothing when the cap is unlimited", () => {
    const rows = [constrained("a", 5)];
    const proposal = proposeRebalance(rows, null);

    expect(proposal.proposals.size).toBe(0);
    expect(proposal.reachedLimit).toBe(true);
  });

  it("lands the org at or under the cap", () => {
    const rows = [
      constrained("a", 5), // 12/min
      constrained("b", 10), // 6/min
      constrained("c", 30), // 2/min
    ];

    const proposal = proposeRebalance(rows, 5);

    expect(proposal.reachedLimit).toBe(true);
    expect(proposal.totalAfter).toBeLessThanOrEqual(5);
  });

  it("snaps every proposed period to a known ladder step", () => {
    const rows = [constrained("a", 45), constrained("b", 7)];
    const proposal = proposeRebalance(rows, 1);

    expect(proposal.proposals.size).toBeGreaterThan(0);
    for (const seconds of proposal.proposals.values()) {
      expect(PERIOD_STEP_SECONDS).toContain(seconds);
    }
  });

  it("stretches the heaviest contributor first", () => {
    const rows = [
      constrained("light", 300), // 0.2/min
      constrained("heavy", 5, 4), // 48/min
    ];

    const proposal = proposeRebalance(rows, 40);

    expect([...proposal.proposals.keys()]).toEqual(["heavy"]);
  });

  it("is deterministic — same input, same proposal", () => {
    const build = () => [
      constrained("a", 5, 2),
      constrained("b", 10),
      constrained("c", 30, 3),
    ];

    const first = proposeRebalance(build(), 3);
    const second = proposeRebalance(build(), 3);

    expect([...first.proposals.entries()].sort()).toEqual(
      [...second.proposals.entries()].sort(),
    );
    expect(first.totalAfter).toBe(second.totalAfter);
  });

  it("is deterministic even when the rows arrive in a different order", () => {
    // Ties are broken on uid, so row ordering must not change the outcome —
    // otherwise the same org would get different advice on every reload.
    const rows = [constrained("a", 60), constrained("b", 60), constrained("c", 60)];
    const shuffled = [rows[2], rows[0], rows[1]];

    const straight = proposeRebalance(rows, 2);
    const scrambled = proposeRebalance(shuffled, 2);

    expect([...straight.proposals.entries()].sort()).toEqual(
      [...scrambled.proposals.entries()].sort(),
    );
  });

  it("never proposes a period beyond the type's maximum", () => {
    const rows = [
      row({
        uid: "d",
        currentPeriodSeconds: 3600,
        periodSeconds: 3600,
        minPeriodSeconds: 3600,
        maxPeriodSeconds: 43200,
      }),
    ];

    const proposal = proposeRebalance(rows, 0);

    for (const seconds of proposal.proposals.values()) {
      expect(seconds).toBeLessThanOrEqual(43200);
    }
    // 60/43200 is still above a cap of 0, so it must ADMIT it failed rather
    // than report a fix it did not deliver.
    expect(proposal.reachedLimit).toBe(false);
  });

  it("reports reachedLimit: false when every check is already maxed out", () => {
    const rows = [
      row({
        uid: "a",
        currentPeriodSeconds: 60,
        periodSeconds: 60,
        minPeriodSeconds: 60,
        maxPeriodSeconds: 60,
      }),
      row({
        uid: "b",
        currentPeriodSeconds: 60,
        periodSeconds: 60,
        minPeriodSeconds: 60,
        maxPeriodSeconds: 60,
      }),
    ];

    const proposal = proposeRebalance(rows, 1);

    expect(proposal.proposals.size).toBe(0);
    expect(proposal.reachedLimit).toBe(false);
    expect(proposal.totalAfter).toBe(2);
  });

  it("leaves disabled rows alone", () => {
    const rows = [
      row({
        uid: "off",
        currentEnabled: false,
        enabled: false,
        currentPeriodSeconds: 5,
        periodSeconds: 5,
      }),
      constrained("on", 5),
    ];

    const proposal = proposeRebalance(rows, 1);

    expect(proposal.proposals.has("off")).toBe(false);
    expect(proposal.proposals.has("on")).toBe(true);
  });

  it("does not mutate the rows it was given", () => {
    const rows = [constrained("a", 5)];
    proposeRebalance(rows, 1);

    expect(rows[0].periodSeconds).toBe(5);
  });

  it("never proposes a period below the type's minimum", () => {
    // A check can sit below its type's current minimum (the constraint was
    // tightened later, or it was created through the API). The proposal must
    // climb to a period the type actually accepts, not to the next rung of the
    // global ladder.
    const rows = [
      row({
        uid: "d",
        regions: [],
        currentPeriodSeconds: 60,
        periodSeconds: 60,
        minPeriodSeconds: 3600,
        maxPeriodSeconds: 86400,
      }),
    ];

    const proposal = proposeRebalance(rows, 0.5);

    expect([...proposal.proposals.values()]).toEqual([3600]);
    expect(proposal.reachedLimit).toBe(true);
  });

  it("only ever lengthens periods — never speeds a check up", () => {
    const rows = [constrained("a", 300), constrained("b", 5)];
    const proposal = proposeRebalance(rows, 1);

    for (const [uid, seconds] of proposal.proposals) {
      const before = rows.find((r) => r.uid === uid)!.periodSeconds;
      expect(seconds).toBeGreaterThan(before);
    }
  });
});

describe("formatRate", () => {
  it("keeps whole numbers whole and fractions to one decimal", () => {
    expect(formatRate(12)).toBe("12");
    expect(formatRate(0.2)).toBe("0.2");
    expect(formatRate(1.25)).toBe("1.3");
  });
});

describe("describePeriod", () => {
  it("picks the largest whole unit", () => {
    expect(describePeriod(30)).toEqual({ unit: "seconds", count: 30 });
    expect(describePeriod(300)).toEqual({ unit: "minutes", count: 5 });
    expect(describePeriod(3600)).toEqual({ unit: "hours", count: 1 });
    expect(describePeriod(86400)).toEqual({ unit: "days", count: 1 });
    expect(describePeriod(604800)).toEqual({ unit: "weeks", count: 1 });
    expect(describePeriod(2592000)).toEqual({ unit: "days", count: 30 });
  });

  it("falls back to seconds for a period no larger unit divides", () => {
    expect(describePeriod(45)).toEqual({ unit: "seconds", count: 45 });
    expect(describePeriod(90)).toEqual({ unit: "seconds", count: 90 });
  });
});

describe("periodToHMS", () => {
  it("round-trips through the API wire format", () => {
    expect(periodToHMS(300)).toBe("00:05:00");
    expect(periodToHMS(86400)).toBe("24:00:00");
    expect(hmsToSeconds(periodToHMS(45))).toBe(45);
  });
});

// ---------------------------------------------------------------------------
// Locale completeness. A key missing from one bundle renders the raw key path
// in that language — a visible defect that no type check catches.
// ---------------------------------------------------------------------------

const LOCALES = {
  en: enChecks,
  fr: frChecks,
  de: deChecks,
  es: esChecks,
};

/** Flat keys under `scheduling`, with the placeholders each must interpolate. */
const REQUIRED_PLACEHOLDERS: Record<string, string[]> = {
  title: [],
  subtitle: [],
  backToChecks: [],
  autoRebalance: [],
  apply: [],
  reset: [],
  pending_one: ["{{count}}"],
  pending_other: ["{{count}}"],
  proposed: [],
  customPeriod: ["{{period}}"],
  proposalSummary_one: ["{{count}}", "{{before}}", "{{after}}", "{{limit}}"],
  proposalSummary_other: ["{{count}}", "{{before}}", "{{after}}", "{{limit}}"],
  rebalanceExhausted: [],
  passiveNote_one: ["{{count}}"],
  passiveNote_other: ["{{count}}"],
  "meter.label": [],
  "meter.hint": [],
  "meter.overHint": [],
  "meter.unlimited": [],
  "meter.unlimitedHint": [],
  "table.check": [],
  "table.type": [],
  "table.period": [],
  "table.contribution": [],
  "table.enabled": [],
  "table.periodFor": ["{{name}}"],
  "table.enabledFor": ["{{name}}"],
  "table.regionCount_one": ["{{count}}"],
  "table.regionCount_other": ["{{count}}"],
  "periodUnit.seconds_one": ["{{count}}"],
  "periodUnit.seconds_other": ["{{count}}"],
  "periodUnit.minutes_one": ["{{count}}"],
  "periodUnit.minutes_other": ["{{count}}"],
  "periodUnit.hours_one": ["{{count}}"],
  "periodUnit.hours_other": ["{{count}}"],
  "periodUnit.days_one": ["{{count}}"],
  "periodUnit.days_other": ["{{count}}"],
  "periodUnit.weeks_one": ["{{count}}"],
  "periodUnit.weeks_other": ["{{count}}"],
  "empty.title": [],
  "empty.hint": [],
  "toast.applied_one": ["{{count}}"],
  "toast.applied_other": ["{{count}}"],
  "toast.partial": ["{{applied}}", "{{failed}}", "{{names}}"],
  "toast.failed": [],
};

function lookup(bundle: unknown, path: string): unknown {
  return path
    .split(".")
    .reduce<unknown>(
      (node, part) =>
        node && typeof node === "object"
          ? (node as Record<string, unknown>)[part]
          : undefined,
      (bundle as Record<string, unknown>).scheduling,
    );
}

describe("scheduling locale keys", () => {
  it.each(Object.keys(LOCALES))("%s ships every key, non-empty", (locale) => {
    const bundle = LOCALES[locale as keyof typeof LOCALES];

    for (const key of Object.keys(REQUIRED_PLACEHOLDERS)) {
      const value = lookup(bundle, key);
      expect(typeof value, `${locale}: scheduling.${key}`).toBe("string");
      expect(
        (value as string).trim().length,
        `${locale}: scheduling.${key}`,
      ).toBeGreaterThan(0);
    }
  });

  it.each(Object.keys(LOCALES))(
    "%s keeps every interpolation placeholder",
    (locale) => {
      const bundle = LOCALES[locale as keyof typeof LOCALES];

      for (const [key, placeholders] of Object.entries(REQUIRED_PLACEHOLDERS)) {
        for (const placeholder of placeholders) {
          expect(
            lookup(bundle, key) as string,
            `${locale}: scheduling.${key}`,
          ).toContain(placeholder);
        }
      }
    },
  );

  it("translates the copy rather than copying English into every locale", () => {
    const titles = Object.values(LOCALES).map(
      (bundle) =>
        lookup(bundle, "title") as string,
    );

    expect(new Set(titles).size).toBe(titles.length);
  });
});
