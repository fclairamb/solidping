import { describe, expect, it } from "vitest";

import { acceptedConfig, dnsChangesOf } from "./dns-changes-card";

describe("dnsChangesOf", () => {
  it("reads added and removed values", () => {
    expect(
      dnsChangesOf({ changes: { added: ["ns3.evil.com"], removed: ["ns2.acme.com"] } }),
    ).toEqual({ added: ["ns3.evil.com"], removed: ["ns2.acme.com"] });
  });

  it("is null without changes", () => {
    expect(dnsChangesOf(undefined)).toBeNull();
    expect(dnsChangesOf({ baseline_capture: ["ns1.acme.com"] })).toBeNull();
    expect(dnsChangesOf({ changes: { added: [], removed: [] } })).toBeNull();
    expect(dnsChangesOf({ changes: ["x"] })).toBeNull();
  });
});

describe("acceptedConfig", () => {
  it("keeps the config and empties the baseline", () => {
    expect(
      acceptedConfig({ host: "acme.com", detect_changes: true, baseline: { eu: ["a"] } }),
    ).toEqual({ host: "acme.com", detect_changes: true, baseline: {} });
  });
});
