import { describe, expect, it } from "vitest";

import {
  dnsModule,
  domainModule,
  isValidIPv4,
  isValidIPv6,
  type DnsState,
  type DomainState,
} from "./dns";

// Base state for toConfig tests — a valid domain and defaults, so each test
// only needs to override what it's exercising.
function baseState(overrides: Partial<DnsState> = {}): DnsState {
  return {
    host: "one.one.one.one",
    nameserver: "",
    recordType: "A",
    expectedIps: [],
    expectedValues: "",
    ...overrides,
  };
}

describe("dnsModule.fromConfig — expectation seeding", () => {
  it("seeds expected_ips into chips and expected_values into the textarea", () => {
    const state = dnsModule.fromConfig({
      host: "one.one.one.one",
      expected_ips: ["1.1.1.1", "1.0.0.1"],
    });
    expect(state.expectedIps).toEqual(["1.1.1.1", "1.0.0.1"]);
    expect(state.expectedValues).toBe("");
  });

  it("seeds expected_values as one line per entry", () => {
    const state = dnsModule.fromConfig({
      host: "example.com",
      record_type: "MX",
      expected_values: ["10 mail.example.com", "20 backup.example.com"],
    });
    expect(state.expectedValues).toBe(
      "10 mail.example.com\n20 backup.example.com",
    );
    expect(state.expectedIps).toEqual([]);
  });

  it("defaults both expectations to empty when the keys are absent", () => {
    const state = dnsModule.fromConfig({ host: "example.com" });
    expect(state.expectedIps).toEqual([]);
    expect(state.expectedValues).toBe("");
  });
});

describe("dnsModule.toConfig — expectation serialization", () => {
  it("writes expected_ips for A records and never expected_values", () => {
    const { config, errors } = dnsModule.toConfig(
      baseState({ expectedIps: ["1.1.1.1"], expectedValues: "stale.example." }),
    );
    expect(config.expected_ips).toEqual(["1.1.1.1"]);
    expect(config.expected_values).toBeUndefined();
    expect(errors).toEqual([]);
  });

  it("writes expected_values for non-IP record types and never expected_ips", () => {
    const { config, errors } = dnsModule.toConfig(
      baseState({
        recordType: "MX",
        expectedIps: ["1.1.1.1"],
        expectedValues: "10 mail.example.com\n20 backup.example.com\n",
      }),
    );
    expect(config.expected_values).toEqual([
      "10 mail.example.com",
      "20 backup.example.com",
    ]);
    expect(config.expected_ips).toBeUndefined();
    expect(errors).toEqual([]);
  });

  it("omits both keys when the expectations are empty", () => {
    const { config } = dnsModule.toConfig(baseState());
    expect(config.expected_ips).toBeUndefined();
    expect(config.expected_values).toBeUndefined();
  });

  it("round-trips a config through fromConfig → toConfig unchanged", () => {
    const original = {
      host: "one.one.one.one",
      nameserver: "1.1.1.1:53",
      expected_ips: ["2.2.2.2"],
    };
    const { config } = dnsModule.toConfig(dnsModule.fromConfig(original));
    expect(config).toEqual(original);
  });

  it("flags a non-IPv4 chip on an A record as a blocking error", () => {
    const { errors } = dnsModule.toConfig(
      baseState({ expectedIps: ["1.1.1.1", "not-an-ip"] }),
    );
    expect(errors).toHaveLength(1);
    expect(errors[0].name).toBe("expected_ips");
    expect(errors[0].message).toContain("not-an-ip");
    expect(errors[0].message).toContain("IPv4");
  });

  it("validates AAAA chips as IPv6", () => {
    const ok = dnsModule.toConfig(
      baseState({ recordType: "AAAA", expectedIps: ["2606:4700::1111"] }),
    );
    expect(ok.errors).toEqual([]);
    const bad = dnsModule.toConfig(
      baseState({ recordType: "AAAA", expectedIps: ["1.1.1.1"] }),
    );
    expect(bad.errors).toHaveLength(1);
    expect(bad.errors[0].message).toContain("IPv6");
  });
});

function baseDomainState(overrides: Partial<DomainState> = {}): DomainState {
  return {
    domain: "example.com",
    method: "",
    warningDays: "",
    criticalDays: "",
    ...overrides,
  };
}

describe("domainModule.fromConfig — threshold seeding", () => {
  it("seeds warningDays/criticalDays from the canonical camelCase keys", () => {
    const state = domainModule.fromConfig({
      domain: "example.com",
      warningDays: 30,
      criticalDays: 7,
    });
    expect(state.warningDays).toBe("30");
    expect(state.criticalDays).toBe("7");
  });

  it("falls back to the legacy threshold_days for criticalDays when criticalDays is absent", () => {
    const state = domainModule.fromConfig({
      domain: "example.com",
      threshold_days: 14,
    });
    expect(state.criticalDays).toBe("14");
    expect(state.warningDays).toBe("");
  });
});

describe("domainModule.toConfig — threshold serialization", () => {
  it("omits warningDays/criticalDays when blank (backend applies defaults)", () => {
    const { config } = domainModule.toConfig(baseDomainState());
    expect(config.warningDays).toBeUndefined();
    expect(config.criticalDays).toBeUndefined();
  });

  it("writes numeric warningDays/criticalDays when set", () => {
    const { config } = domainModule.toConfig(
      baseDomainState({ warningDays: "30", criticalDays: "7" }),
    );
    expect(config.warningDays).toBe(30);
    expect(config.criticalDays).toBe(7);
  });
});

describe("IP validators", () => {
  it("isValidIPv4", () => {
    expect(isValidIPv4("1.1.1.1")).toBe(true);
    expect(isValidIPv4("255.255.255.255")).toBe(true);
    expect(isValidIPv4("256.1.1.1")).toBe(false);
    expect(isValidIPv4("1.1.1")).toBe(false);
    expect(isValidIPv4("2606:4700::1111")).toBe(false);
    expect(isValidIPv4("example.com")).toBe(false);
  });

  it("isValidIPv6", () => {
    expect(isValidIPv6("2606:4700::1111")).toBe(true);
    expect(isValidIPv6("::1")).toBe(true);
    expect(isValidIPv6("2001:0db8:85a3:0000:0000:8a2e:0370:7334")).toBe(true);
    expect(isValidIPv6("1.1.1.1")).toBe(false);
    expect(isValidIPv6("2001:db8")).toBe(false);
    expect(isValidIPv6("g::1")).toBe(false);
    expect(isValidIPv6("1::2::3")).toBe(false);
  });
});
