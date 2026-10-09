import { describe, expect, it } from "vitest";

import { isFieldTouched } from "./touched-fields";

describe("isFieldTouched", () => {
  it("matches by id or test id", () => {
    expect(isFieldTouched(["host"], "host")).toBe(true);
    expect(isFieldTouched(["checkportinput"], "port")).toBe(true);
  });
  it("does not match untouched fields", () => {
    expect(isFieldTouched(["host"], "port")).toBe(false);
    expect(isFieldTouched([], "host")).toBe(false);
  });
});
