import { describe, expect, test } from "bun:test";
import { isSaasConfig } from "./deployment";

describe("isSaasConfig", () => {
  test("the cloud's config document", () => {
    expect(isSaasConfig({ runMode: "", deploymentMode: "saas" })).toBe(true);
  });

  test("anything else is self-hosted", () => {
    for (const config of [
      { runMode: "", deploymentMode: "self-hosted" },
      { deploymentMode: "SaaS" },
      {},
      null,
      undefined,
      "saas",
      ["saas"],
    ]) {
      expect(isSaasConfig(config)).toBe(false);
    }
  });
});
