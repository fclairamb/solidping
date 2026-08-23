import { describe, expect, it } from "vitest";

import { mergeResultTiers } from "@/lib/result-tiers";

interface Row {
  uid: string;
  periodStart?: string;
  periodType?: string;
}

/** Reproduces the server's `ORDER BY result.period_start DESC, result.uid DESC`
 * — the ordering the single mixed query used to return, and the reference the
 * merge has to match. Written independently of the implementation on purpose. */
function serverOrder(rows: Row[]): Row[] {
  return [...rows].sort((a, b) => {
    const at = Date.parse(a.periodStart ?? "");
    const bt = Date.parse(b.periodStart ?? "");
    if (at !== bt) return bt - at;

    return a.uid < b.uid ? 1 : a.uid > b.uid ? -1 : 0;
  });
}

function at(minutesAgo: number): string {
  return new Date(
    Date.UTC(2026, 7, 22, 12, 0, 0) - minutesAgo * 60_000,
  ).toISOString();
}

describe("mergeResultTiers", () => {
  // Merged-series equivalence: what the split fetch produces must be exactly
  // what the single `periodType=raw,hour` query produced (spec 2026-08-22-04).
  it("reproduces the single mixed query's row order", () => {
    const rollup: Row[] = [
      { uid: "h-3", periodStart: at(180), periodType: "hour" },
      { uid: "h-2", periodStart: at(240), periodType: "hour" },
      { uid: "h-1", periodStart: at(300), periodType: "hour" },
    ];
    const raw: Row[] = [
      { uid: "r-3", periodStart: at(1), periodType: "raw" },
      { uid: "r-2", periodStart: at(2), periodType: "raw" },
      { uid: "r-1", periodStart: at(120), periodType: "raw" },
    ];

    const merged = mergeResultTiers<Row>([rollup, raw]);

    expect(merged).toEqual(serverOrder([...rollup, ...raw]));
    // And explicitly: strictly non-increasing period_start, newest first.
    const times = merged.map((r) => Date.parse(r.periodStart!));
    expect(times).toEqual([...times].sort((a, b) => b - a));
    expect(merged[0].uid).toBe("r-3");
    expect(merged[merged.length - 1].uid).toBe("h-1");
  });

  // Two regions probe the same check at the same instant, so equal
  // period_start values are the normal case, not an edge case — the SQL
  // ORDER BY breaks the tie on uid DESC and so must the merge.
  it("breaks period_start ties on uid, descending", () => {
    const a: Row[] = [
      { uid: "aaa", periodStart: at(5) },
      { uid: "ccc", periodStart: at(5) },
    ];
    const b: Row[] = [{ uid: "bbb", periodStart: at(5) }];

    expect(mergeResultTiers<Row>([a, b]).map((r) => r.uid)).toEqual([
      "ccc",
      "bbb",
      "aaa",
    ]);
  });

  it("is order-independent across the tier arrays it is given", () => {
    const rollup: Row[] = [{ uid: "h-1", periodStart: at(90) }];
    const raw: Row[] = [
      { uid: "r-2", periodStart: at(1) },
      { uid: "r-1", periodStart: at(30) },
    ];

    expect(mergeResultTiers<Row>([rollup, raw])).toEqual(
      mergeResultTiers<Row>([raw, rollup]),
    );
  });

  it("tolerates missing and still-loading tiers", () => {
    const raw: Row[] = [{ uid: "r-1", periodStart: at(1) }];

    expect(mergeResultTiers<Row>([undefined, raw])).toEqual(raw);
    expect(mergeResultTiers<Row>([undefined, undefined])).toEqual([]);
    expect(mergeResultTiers<Row>([])).toEqual([]);
  });

  // uid is the results primary key and a row has exactly one period_type, so a
  // uid returned by two tiers is a duplicate, never two measurements.
  it("never emits the same uid twice", () => {
    const shared: Row = { uid: "dup", periodStart: at(10) };

    expect(
      mergeResultTiers<Row>([[shared], [shared]]).map((r) => r.uid),
    ).toEqual(["dup"]);
  });

  it("sorts rows with no period_start last rather than dropping them", () => {
    const rows: Row[] = [
      { uid: "no-ts" },
      { uid: "has-ts", periodStart: at(10) },
    ];

    expect(mergeResultTiers<Row>([rows]).map((r) => r.uid)).toEqual([
      "has-ts",
      "no-ts",
    ]);
  });
});
