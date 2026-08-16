import { describe, test, expect } from "bun:test";
import { segmentGeometry } from "./segment-layout";

// The availability bar looked "dirty" because 30 `flex-1` boxes and a fixed 3px
// gap cannot divide an arbitrary row width, and box backgrounds are painted on
// whole device pixels — so the browser kept the gaps and paid for the fraction
// out of the bars. These pin the geometry that replaced it: every segment
// identical, every gap identical, the row filled exactly, and no rounding
// anywhere (the SVG antialiases the fractions instead of snapping them).

const TARGET_GAP = 3;
const EPSILON = 1e-9;

describe("segmentGeometry", () => {
  test("segments fill the row exactly, at the live 30-day width", () => {
    const g = segmentGeometry(686, 30, TARGET_GAP)!;
    expect(g.gap).toBe(TARGET_GAP);
    // Last segment's right edge lands on the row's right edge.
    expect(29 * g.pitch + g.width).toBeCloseTo(686, 9);
    expect(g.width).toBeCloseTo((686 - 29 * TARGET_GAP) / 30, 9);
  });

  test("every segment and every gap is identical, at any width and count", () => {
    for (const count of [24, 30, 90]) {
      for (let width = 200; width <= 1400; width += 3) {
        const g = segmentGeometry(width, count, TARGET_GAP)!;
        // Uniformity is structural: one width, one gap, a constant pitch. The
        // check that matters is that the constant pitch still lands the last
        // segment on the row's edge — no accumulated drift, no overflow.
        expect((count - 1) * g.pitch + g.width).toBeCloseTo(width, 6);
        expect(g.pitch - g.width).toBeCloseTo(g.gap, 9);
        expect(g.width).toBeGreaterThan(0);
        expect(g.gap).toBeGreaterThanOrEqual(0);
      }
    }
  });

  test("keeps the full gap while there is room for it", () => {
    // 30 segments in 686px is ~23px of pitch — plenty for a 3px gap.
    expect(segmentGeometry(686, 30, TARGET_GAP)!.gap).toBe(TARGET_GAP);
    // Still comfortable on a phone-width card.
    expect(segmentGeometry(343, 30, TARGET_GAP)!.gap).toBe(TARGET_GAP);
  });

  test("the gap gives way before the segment does, when segments get thin", () => {
    // 90 daily segments on a narrow card: ~2.4px of pitch. A 3px gap would
    // leave nothing to see, so the gap shrinks and the segment survives.
    const g = segmentGeometry(220, 90, TARGET_GAP)!;
    expect(g.gap).toBeLessThan(TARGET_GAP);
    expect(g.width).toBeGreaterThan(g.gap);
    // The colour still occupies most of the pitch, which is what makes the bar
    // readable at that density.
    expect(g.width / g.pitch).toBeGreaterThan(0.6);
    expect(89 * g.pitch + g.width).toBeCloseTo(220, 6);
  });

  test("a single segment takes the whole row, with no gap to speak of", () => {
    const g = segmentGeometry(500, 1, TARGET_GAP)!;
    expect(g.width).toBeCloseTo(500, 9);
  });

  test("null for nothing to lay out or an unmeasured row", () => {
    expect(segmentGeometry(0, 30, TARGET_GAP)).toBeNull();
    expect(segmentGeometry(NaN, 30, TARGET_GAP)).toBeNull();
    expect(segmentGeometry(-10, 30, TARGET_GAP)).toBeNull();
    expect(segmentGeometry(686, 0, TARGET_GAP)).toBeNull();
  });

  test("nothing is rounded — fractional widths survive as fractions", () => {
    const g = segmentGeometry(687.4, 30, TARGET_GAP)!;
    expect(Number.isInteger(g.width)).toBe(false);
    expect(Math.abs(29 * g.pitch + g.width - 687.4)).toBeLessThan(EPSILON);
  });
});
