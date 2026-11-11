import { describe, expect, it } from "vitest";
import { glanceStripWindow } from "./glance-strip-window";

// The glance strips used to show the last 24 hours whatever window the
// dashboard header was set to. They must follow the selected window.
describe("glanceStripWindow", () => {
  const now = Date.parse("2026-11-10T14:19:58Z");
  const cells = (w: ReturnType<typeof glanceStripWindow>) =>
    (Date.parse(w.to) - Date.parse(w.from)) / (w.bucketSeconds * 1000);

  it("24h: 24 hourly cells ending with the current hour", () => {
    const w = glanceStripWindow("24h", now);
    expect(w).toEqual({
      from: "2026-11-09T15:00:00.000Z",
      to: "2026-11-10T15:00:00.000Z",
      bucketSeconds: 3600,
    });
  });

  it("7d: 28 six-hour cells covering the last 7 days", () => {
    const w = glanceStripWindow("7d", now);
    expect(w.bucketSeconds).toBe(6 * 3600);
    expect(cells(w)).toBe(28);
    expect(w.to).toBe("2026-11-10T18:00:00.000Z");
    expect(w.from).toBe("2026-11-03T18:00:00.000Z");
  });

  it("30d: 30 daily cells aligned on UTC midnight", () => {
    const w = glanceStripWindow("30d", now);
    expect(w.bucketSeconds).toBe(86400);
    expect(cells(w)).toBe(30);
    expect(w.to).toBe("2026-11-11T00:00:00.000Z");
    expect(w.from).toBe("2026-10-12T00:00:00.000Z");
  });
});
