// Geometry for the availability bar's day/hour segments.
//
// Why this exists at all: the bar used to be 30 `flex-1` divs with a 3px `gap`,
// and that CANNOT space evenly. 30 segments and 29 gaps in a 686px row leaves
// 19.97px per segment, and a browser paints box backgrounds on whole device
// pixels — so it keeps the gaps and pays for the fraction out of the bars.
// Measured across widths and device pixel ratios, the result is bars that
// differ by a full CSS pixel and a bar-to-bar pitch that differs by half of
// one; on a fractional ratio (browser zoom, a scaled display) that lands on
// roughly every other bar. A too-thin bar reads as extra space beside it, which
// is exactly the "unevenly spaced" look being fixed here.
//
// The bar is drawn as one SVG instead, where the segments are shapes rather
// than boxes: they keep their exact fractional geometry and get antialiased
// rather than snapped, so every segment is identical to within a fraction of a
// device pixel at every ratio. This module is the fractional maths behind it —
// no rounding anywhere, on purpose.

// Below this, a segment is too thin to keep the full gap around it (very narrow
// screens, or a 90-day window): the gap gives way rather than the bar.
const MIN_BAR_TO_GAP_RATIO = 2;

export interface SegmentGeometry {
  /** Distance between the left edges of two consecutive segments. */
  pitch: number;
  /** Width of every segment — identical, by construction. */
  width: number;
  /** Space between two segments — identical, by construction. */
  gap: number;
}

/**
 * Lays `count` identical segments across `rowWidth`, separated by a gap of at
 * most `targetGap`. The segments fill the row exactly: segment i spans
 * `[i * pitch, i * pitch + width]`, and the last one ends on `rowWidth`.
 *
 * Returns null when there is nothing to lay out.
 */
export function segmentGeometry(
  rowWidth: number,
  count: number,
  targetGap: number,
): SegmentGeometry | null {
  if (!Number.isFinite(rowWidth) || rowWidth <= 0) return null;
  if (!Number.isFinite(count) || count <= 0) return null;

  // Keep the gap from eating the bar when segments get thin: at that point the
  // row reads as a stripe of colour and the separation matters less than the
  // colour being visible at all.
  const perSegment = rowWidth / count;
  const gap = Math.max(
    0,
    Math.min(targetGap, perSegment / (MIN_BAR_TO_GAP_RATIO + 1)),
  );

  // count * pitch - gap === rowWidth, i.e. the row is filled to the edge.
  const pitch = (rowWidth + gap) / count;
  return { pitch, width: pitch - gap, gap };
}
