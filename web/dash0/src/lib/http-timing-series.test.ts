import { describe, expect, it } from "vitest";

import { buildTimingSeries } from "./http-timing-series";

describe("buildTimingSeries", () => {
  it("averages per phase over the rows that carry it, sorted by time", () => {
    const series = buildTimingSeries([
      { periodStart: "2026-10-12T10:01:00Z", metrics: { tls_time_ms: 10 } },
      {
        periodStart: "2026-10-12T10:00:00Z",
        metrics: { tls_time_ms: 20, connect_time_ms: 4 },
      },
      { periodStart: "2026-10-12T10:00:00Z", metrics: { connect_time_ms: 8 } },
      { periodStart: "2026-10-12T10:02:00Z", metrics: null },
    ]);
    expect(series).toHaveLength(2);
    expect(series[0].ts).toBeLessThan(series[1].ts);
    expect(series[0].tls).toBe(20);
    expect(series[0].connect).toBe(6);
    expect(series[1].connect).toBeUndefined();
  });
});
