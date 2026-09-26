import { describe, expect, it } from "vitest";

import { checkTypeCanCapture } from "./check-screenshots";

describe("checkTypeCanCapture", () => {
  it("is true for the two types that can capture", () => {
    expect(checkTypeCanCapture("browser")).toBe(true);
    expect(checkTypeCanCapture("js")).toBe(true);
  });

  it("is false for every other type, and for a missing one", () => {
    expect(checkTypeCanCapture("http")).toBe(false);
    expect(checkTypeCanCapture("rdp")).toBe(false);
    expect(checkTypeCanCapture(undefined)).toBe(false);
  });
});
