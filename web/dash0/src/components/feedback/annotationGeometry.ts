import type { Annotation } from "./types";

export interface Size {
  width: number;
  height: number;
}

export interface PixelRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

const TEXT_HEIGHT = 16;
const TEXT_CHAR_WIDTH = 8.5;
const HIT_TOLERANCE = 10;

// normalizeRect turns a rect with possibly negative w/h into one with positive
// extents.
export function normalizeRect(r: PixelRect): PixelRect {
  return {
    x: r.w < 0 ? r.x + r.w : r.x,
    y: r.h < 0 ? r.y + r.h : r.y,
    w: Math.abs(r.w),
    h: Math.abs(r.h),
  };
}

// boundsOf returns the pixel bounding box of an annotation.
export function boundsOf(ann: Annotation, size: Size): PixelRect {
  const W = size.width;
  const H = size.height;
  switch (ann.type) {
    case "rect":
    case "highlight":
    case "blur":
      return normalizeRect({ x: ann.x * W, y: ann.y * H, w: ann.w * W, h: ann.h * H });
    case "arrow":
      return normalizeRect({
        x: ann.x * W,
        y: ann.y * H,
        w: (ann.x2 - ann.x) * W,
        h: (ann.y2 - ann.y) * H,
      });
    case "pen": {
      const xs = ann.points.map((p) => p.x * W);
      const ys = ann.points.map((p) => p.y * H);
      const minX = Math.min(...xs);
      const minY = Math.min(...ys);
      return { x: minX, y: minY, w: Math.max(...xs) - minX, h: Math.max(...ys) - minY };
    }
    case "text":
      return {
        x: ann.x * W,
        y: ann.y * H - TEXT_HEIGHT,
        w: ann.text.length * TEXT_CHAR_WIDTH,
        h: TEXT_HEIGHT + 4,
      };
  }
}

function distToSegment(
  px: number,
  py: number,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
): number {
  const dx = x2 - x1;
  const dy = y2 - y1;
  const len2 = dx * dx + dy * dy;
  const t = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((px - x1) * dx + (py - y1) * dy) / len2));
  return Math.hypot(px - (x1 + t * dx), py - (y1 + t * dy));
}

// hitTest reports whether the normalized point (x, y) hits the annotation.
export function hitTest(ann: Annotation, x: number, y: number, size: Size): boolean {
  const px = x * size.width;
  const py = y * size.height;

  if (ann.type === "arrow") {
    return (
      distToSegment(
        px,
        py,
        ann.x * size.width,
        ann.y * size.height,
        ann.x2 * size.width,
        ann.y2 * size.height,
      ) <= HIT_TOLERANCE
    );
  }
  if (ann.type === "pen") {
    const pts = ann.points;
    if (pts.length === 1) {
      return Math.hypot(px - pts[0].x * size.width, py - pts[0].y * size.height) <= HIT_TOLERANCE;
    }
    for (let i = 1; i < pts.length; i++) {
      const d = distToSegment(
        px,
        py,
        pts[i - 1].x * size.width,
        pts[i - 1].y * size.height,
        pts[i].x * size.width,
        pts[i].y * size.height,
      );
      if (d <= HIT_TOLERANCE) return true;
    }
    return false;
  }

  const b = boundsOf(ann, size);
  return (
    px >= b.x - HIT_TOLERANCE / 2 &&
    px <= b.x + b.w + HIT_TOLERANCE / 2 &&
    py >= b.y - HIT_TOLERANCE / 2 &&
    py <= b.y + b.h + HIT_TOLERANCE / 2
  );
}

// topmostHit returns the index of the last (topmost) annotation hit, or -1.
export function topmostHit(
  annotations: Annotation[],
  x: number,
  y: number,
  size: Size,
): number {
  for (let i = annotations.length - 1; i >= 0; i--) {
    if (hitTest(annotations[i], x, y, size)) return i;
  }
  return -1;
}

// translateAnnotation moves an annotation by a normalized delta.
export function translateAnnotation(ann: Annotation, dx: number, dy: number): Annotation {
  switch (ann.type) {
    case "arrow":
      return { ...ann, x: ann.x + dx, y: ann.y + dy, x2: ann.x2 + dx, y2: ann.y2 + dy };
    case "pen":
      return {
        ...ann,
        x: ann.x + dx,
        y: ann.y + dy,
        points: ann.points.map((p) => ({ x: p.x + dx, y: p.y + dy })),
      };
    default:
      return { ...ann, x: ann.x + dx, y: ann.y + dy };
  }
}

export interface PixelBuffer {
  data: Uint8ClampedArray;
  width: number;
  height: number;
}

// pixelate replaces every block x block cell inside `rect` with its average
// colour, in place. Pixels outside the rect are never touched.
export function pixelate(img: PixelBuffer, rect: PixelRect, block: number): void {
  const x0 = Math.max(0, Math.floor(rect.x));
  const y0 = Math.max(0, Math.floor(rect.y));
  const x1 = Math.min(img.width, Math.ceil(rect.x + rect.w));
  const y1 = Math.min(img.height, Math.ceil(rect.y + rect.h));
  const step = Math.max(1, Math.floor(block));

  for (let by = y0; by < y1; by += step) {
    for (let bx = x0; bx < x1; bx += step) {
      const ex = Math.min(bx + step, x1);
      const ey = Math.min(by + step, y1);
      const sum = [0, 0, 0, 0];
      let n = 0;
      for (let y = by; y < ey; y++) {
        for (let x = bx; x < ex; x++) {
          const i = (y * img.width + x) * 4;
          for (let c = 0; c < 4; c++) sum[c] += img.data[i + c];
          n++;
        }
      }
      if (n === 0) continue;
      for (let y = by; y < ey; y++) {
        for (let x = bx; x < ex; x++) {
          const i = (y * img.width + x) * 4;
          for (let c = 0; c < 4; c++) img.data[i + c] = Math.round(sum[c] / n);
        }
      }
    }
  }
}
