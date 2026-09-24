import { describe, expect, it } from "vitest";

import { rollupSectionStatus } from "@/lib/status-rollup";

describe("rollupSectionStatus", () => {
  it("ranks down > validating > warning > stale > up (spec 2026-09-25-02)", () => {
    expect(rollupSectionStatus({ down: 2 })).toBe("down");
    expect(rollupSectionStatus({ down: 1, stale: 3 })).toBe("degraded");
    expect(rollupSectionStatus({ validating: 1, warning: 1 })).toBe("validating");
    expect(rollupSectionStatus({ warning: 1, stale: 1 })).toBe("warning");
    expect(rollupSectionStatus({ stale: 1, up: 5 })).toBe("stale");
    expect(rollupSectionStatus({ up: 5 })).toBe("up");
  });

  it("reads an all-stale section as stale, never created", () => {
    expect(rollupSectionStatus({ stale: 4 })).toBe("stale");
  });

  it("reads an empty or created-only section as created", () => {
    expect(rollupSectionStatus({})).toBe("created");
    expect(rollupSectionStatus({ created: 2 })).toBe("created");
  });
});
