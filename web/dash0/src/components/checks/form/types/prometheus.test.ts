import { describe, expect, it } from "vitest";

import { prometheusModule, type PrometheusState } from "./infra";

function baseState(overrides: Partial<PrometheusState> = {}): PrometheusState {
  return {
    url: "https://app.example.com/metrics",
    mode: "scrape",
    metric: "process_open_fds",
    labels: [],
    query: "",
    operator: ">",
    warningValue: "800",
    criticalValue: "1000",
    match: "single",
    onMissing: "down",
    headers: [],
    ...overrides,
  };
}

describe("prometheusModule.toConfig", () => {
  it("serializes a scrape config with labels", () => {
    const { config, errors } = prometheusModule.toConfig(
      baseState({ labels: [{ key: "instance", value: "app-1" }] }),
    );
    expect(errors).toEqual([]);
    expect(config).toMatchObject({
      url: "https://app.example.com/metrics",
      mode: "scrape",
      metric: "process_open_fds",
      labels: { instance: "app-1" },
      operator: ">",
      warningValue: 800,
      criticalValue: 1000,
    });
    // Defaults stay out of the payload.
    expect(config.match).toBeUndefined();
    expect(config.onMissing).toBeUndefined();
    expect(config.query).toBeUndefined();
  });

  it("serializes a promql config and drops the scrape selector", () => {
    const { config, errors } = prometheusModule.toConfig(
      baseState({
        mode: "promql",
        url: "https://prometheus.example.com",
        query: "sum(rate(http_requests_total[5m]))",
        metric: "leftover",
        labels: [{ key: "instance", value: "app-1" }],
      }),
    );
    expect(errors).toEqual([]);
    expect(config.query).toBe("sum(rate(http_requests_total[5m]))");
    expect(config.metric).toBeUndefined();
    expect(config.labels).toBeUndefined();
  });

  // The pointer-presence trap, frontend half: "0" is a real threshold and must
  // reach the payload; "" is what "unset" looks like.
  it("keeps a 0 threshold and omits an empty one", () => {
    const { config, errors } = prometheusModule.toConfig(
      baseState({ operator: "<=", warningValue: "0", criticalValue: "" }),
    );
    expect(errors).toEqual([]);
    expect(config.warningValue).toBe(0);
    expect(config).not.toHaveProperty("criticalValue");
  });

  it("reports required fields", () => {
    expect(
      prometheusModule
        .toConfig(baseState({ url: "" }))
        .errors.map((e) => e.name),
    ).toContain("url");

    expect(
      prometheusModule
        .toConfig(baseState({ metric: "" }))
        .errors.map((e) => e.name),
    ).toContain("metric");

    expect(
      prometheusModule
        .toConfig(baseState({ mode: "promql", query: "" }))
        .errors.map((e) => e.name),
    ).toContain("query");

    expect(
      prometheusModule
        .toConfig(baseState({ warningValue: "", criticalValue: "" }))
        .errors.map((e) => e.name),
    ).toContain("criticalValue");
  });

  it("accepts a warning-only config (it can never page, by design)", () => {
    const { errors } = prometheusModule.toConfig(
      baseState({ criticalValue: "" }),
    );
    expect(errors).toEqual([]);
  });
});

describe("prometheusModule.fromConfig", () => {
  it("round-trips a scrape config", () => {
    const config = {
      url: "https://app.example.com/metrics",
      mode: "scrape",
      metric: "queue_depth",
      labels: { queue: "a", dc: "eu" },
      operator: "<",
      warningValue: 10,
      criticalValue: 2,
      match: "max",
      onMissing: "warning",
      headers: { Authorization: "Bearer t" },
    };
    const state = prometheusModule.fromConfig(config);
    expect(state.metric).toBe("queue_depth");
    expect(state.operator).toBe("<");
    expect(state.warningValue).toBe("10");
    expect(state.criticalValue).toBe("2");
    expect(state.match).toBe("max");
    expect(state.onMissing).toBe("warning");
    expect(state.labels).toEqual(
      expect.arrayContaining([
        { key: "queue", value: "a" },
        { key: "dc", value: "eu" },
      ]),
    );
    expect(state.headers).toEqual([
      { key: "Authorization", value: "Bearer t" },
    ]);

    // And back out unchanged.
    expect(prometheusModule.toConfig(state).config).toMatchObject(config);
  });

  it("seeds a 0 threshold as a visible value, not as blank", () => {
    const state = prometheusModule.fromConfig({
      url: "https://x/metrics",
      metric: "free_slots",
      operator: "<=",
      warningValue: 0,
    });
    expect(state.warningValue).toBe("0");
    expect(state.criticalValue).toBe("");
  });

  it("applies defaults for an empty config", () => {
    const state = prometheusModule.fromConfig({});
    expect(state.mode).toBe("scrape");
    expect(state.operator).toBe(">");
    expect(state.match).toBe("single");
    expect(state.onMissing).toBe("down");
    expect(state.labels).toEqual([]);
    expect(state.headers).toEqual([]);
  });
});
