import { describe, expect, it } from "vitest";

import { hitTest, pixelate, topmostHit, translateAnnotation } from "./annotationGeometry";
import type { Annotation } from "./types";

const size = { width: 200, height: 100 };

describe("pixelate (blur annotation)", () => {
  function gradient(w: number, h: number) {
    const data = new Uint8ClampedArray(w * h * 4);
    for (let i = 0; i < w * h; i++) {
      data[i * 4] = (i * 7) % 256;
      data[i * 4 + 1] = (i * 13) % 256;
      data[i * 4 + 2] = (i * 29) % 256;
      data[i * 4 + 3] = 255;
    }
    return { data, width: w, height: h };
  }

  it("changes pixels inside the region and leaves the rest identical", () => {
    const src = gradient(40, 40);
    const out = { ...src, data: new Uint8ClampedArray(src.data) };
    pixelate(out, { x: 10, y: 10, w: 20, h: 20 }, 8);

    let changedInside = 0;
    for (let y = 0; y < 40; y++) {
      for (let x = 0; x < 40; x++) {
        const i = (y * 40 + x) * 4;
        const same = [0, 1, 2, 3].every((c) => src.data[i + c] === out.data[i + c]);
        const inside = x >= 10 && x < 30 && y >= 10 && y < 30;
        if (inside && !same) changedInside++;
        if (!inside) expect(same).toBe(true);
      }
    }
    expect(changedInside).toBeGreaterThan(0);
  });

  it("makes each block uniform", () => {
    const img = gradient(16, 16);
    pixelate(img, { x: 0, y: 0, w: 16, h: 16 }, 8);
    const px = (x: number, y: number) => img.data[(y * 16 + x) * 4];
    expect(px(0, 0)).toBe(px(7, 7));
  });
});

describe("hit testing and moving", () => {
  const rect: Annotation = { type: "rect", x: 0.1, y: 0.1, w: 0.2, h: 0.2, color: "#f00" };
  const arrow: Annotation = { type: "arrow", x: 0.5, y: 0.5, x2: 0.9, y2: 0.5, color: "#f00" };

  it("hits a rect inside and misses far outside", () => {
    expect(hitTest(rect, 0.2, 0.2, size)).toBe(true);
    expect(hitTest(rect, 0.8, 0.9, size)).toBe(false);
  });

  it("hits an arrow only near its line", () => {
    expect(hitTest(arrow, 0.7, 0.5, size)).toBe(true);
    expect(hitTest(arrow, 0.7, 0.9, size)).toBe(false);
  });

  it("returns the topmost index, or -1", () => {
    expect(topmostHit([rect, rect], 0.2, 0.2, size)).toBe(1);
    expect(topmostHit([rect], 0.9, 0.9, size)).toBe(-1);
  });

  it("translates every point of a pen stroke", () => {
    const pen: Annotation = {
      type: "pen",
      x: 0.1,
      y: 0.1,
      color: "#f00",
      points: [
        { x: 0.1, y: 0.1 },
        { x: 0.2, y: 0.2 },
      ],
    };
    const moved = translateAnnotation(pen, 0.1, 0) as typeof pen;
    expect(moved.points[1].x).toBeCloseTo(0.3);
    expect(moved.x).toBeCloseTo(0.2);
  });
});
