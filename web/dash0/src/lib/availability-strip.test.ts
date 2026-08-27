import { describe, it, expect } from "vitest";
import {
  bucketSecondsForSpan,
  shouldRenderStrip,
  stripBucketSeconds,
  MAX_STRIP_CELLS,
  ONE_HOUR_SECONDS,
  STRIP_MIN_SPAN_MS,
} from "./availability-strip";

const HOUR_MS = 3_600_000;
const DAY_MS = 24 * HOUR_MS;

describe("stripBucketSeconds", () => {
  it("uses the spec's fixed widths for the chart's preset ranges", () => {
    // These are binding, not advisory (spec 2026-08-26-10, resolved question 2):
    // day → 1h (24 cells), week → 6h (28 cells), month → 1d (~30 cells).
    expect(stripBucketSeconds("day")).toBe(ONE_HOUR_SECONDS);
    expect(stripBucketSeconds("week")).toBe(6 * ONE_HOUR_SECONDS);
    expect(stripBucketSeconds("month")).toBe(24 * ONE_HOUR_SECONDS);
  });

  it("yields the documented cell counts for those ranges", () => {
    expect(DAY_MS / (stripBucketSeconds("day") * 1000)).toBe(24);
    expect((7 * DAY_MS) / (stripBucketSeconds("week") * 1000)).toBe(28);
    expect((30 * DAY_MS) / (stripBucketSeconds("month") * 1000)).toBe(30);
  });

  it("switches to the zoom rule as soon as a zoom span is given", () => {
    // A drag-zoom inside the month view must NOT keep the month's 1d cells.
    expect(stripBucketSeconds("month", 6 * HOUR_MS)).toBe(ONE_HOUR_SECONDS);
    expect(stripBucketSeconds("day", 30 * DAY_MS)).toBe(12 * ONE_HOUR_SECONDS);
  });
});

describe("bucketSecondsForSpan", () => {
  it("never goes below the one-hour floor", () => {
    // Below an hour a percentage is noise: a 5-minute slice of a 1-minute check
    // quantizes to 0/20/40/…%.
    for (const spanMs of [0, -1, 1000, 60_000, HOUR_MS, 3 * HOUR_MS]) {
      expect(bucketSecondsForSpan(spanMs)).toBeGreaterThanOrEqual(
        ONE_HOUR_SECONDS,
      );
    }
    expect(bucketSecondsForSpan(Number.NaN)).toBe(ONE_HOUR_SECONDS);
  });

  it("always emits a whole multiple of one hour", () => {
    for (const hours of [1, 5, 13, 47, 61, 200, 24 * 90]) {
      expect(bucketSecondsForSpan(hours * HOUR_MS) % ONE_HOUR_SECONDS).toBe(0);
    }
  });

  it("keeps the cell count at or below the ceiling", () => {
    for (const hours of [1, 24, 59, 60, 61, 168, 720, 24 * 90]) {
      const seconds = bucketSecondsForSpan(hours * HOUR_MS);
      const cells = Math.ceil((hours * HOUR_MS) / (seconds * 1000));
      expect(cells).toBeLessThanOrEqual(MAX_STRIP_CELLS);
    }
  });

  it("picks the SMALLEST width that fits, not merely one that fits", () => {
    // 61 hours: 1h would be 61 cells (one too many), 2h is 31 — so 2h, and the
    // next width down must genuinely overflow.
    const seconds = bucketSecondsForSpan(61 * HOUR_MS);
    expect(seconds).toBe(2 * ONE_HOUR_SECONDS);
    expect(Math.ceil(61 / (seconds / ONE_HOUR_SECONDS))).toBeLessThanOrEqual(
      MAX_STRIP_CELLS,
    );
    expect(61).toBeGreaterThan(MAX_STRIP_CELLS);
  });
});

describe("shouldRenderStrip", () => {
  it("hides the strip below the 3h floor and shows it at or above", () => {
    expect(shouldRenderStrip(HOUR_MS)).toBe(false);
    expect(shouldRenderStrip(STRIP_MIN_SPAN_MS - 1)).toBe(false);
    expect(shouldRenderStrip(STRIP_MIN_SPAN_MS)).toBe(true);
    expect(shouldRenderStrip(DAY_MS)).toBe(true);
  });

  it("hides it for a nonsensical span rather than dividing by it", () => {
    expect(shouldRenderStrip(0)).toBe(false);
    expect(shouldRenderStrip(Number.NaN)).toBe(false);
  });
});
