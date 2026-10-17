import { describe, expect, it } from "vitest";
import { vendorStatusModule } from "./vendor-status";

describe("vendorStatusModule", () => {
  it("round-trips a stored config", () => {
    const stored = { url: "https://www.githubstatus.com", provider: "github", components: ["abc", "def"] };
    const { config, errors } = vendorStatusModule.toConfig(vendorStatusModule.fromConfig(stored));
    expect(errors).toEqual([]);
    expect(config).toEqual(stored);
  });

  it("watches the whole page when no component is ticked", () => {
    const { config, errors } = vendorStatusModule.toConfig(
      vendorStatusModule.fromConfig({ url: "https://status.acme.com" }),
    );
    expect(errors).toEqual([]);
    expect(config).toEqual({ url: "https://status.acme.com" });
  });

  it("requires an https url", () => {
    expect(vendorStatusModule.toConfig(vendorStatusModule.fromConfig({})).errors).toHaveLength(1);
    const http = vendorStatusModule.toConfig(vendorStatusModule.fromConfig({ url: "http://status.acme.com" }));
    expect(http.errors).toHaveLength(1);
  });
});
