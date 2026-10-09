import { describe, expect, it } from "vitest";
import { secretInputVisible, shouldSendSecret } from "./secret-input";

describe("secretInputVisible", () => {
  it("is visible on first setup", () => {
    expect(secretInputVisible(false, false)).toBe(true);
  });
  it("is hidden when stored and not editing", () => {
    expect(secretInputVisible(false, true)).toBe(false);
  });
  it("is visible when editing", () => {
    expect(secretInputVisible(true, true)).toBe(true);
  });
});

describe("shouldSendSecret", () => {
  it("sends a typed value on first setup", () => {
    expect(shouldSendSecret(false, false, "smtp-key")).toBe(true);
  });
  it("does not send an empty value on first setup", () => {
    expect(shouldSendSecret(false, false, "")).toBe(false);
  });
  it("does not send when stored and not editing", () => {
    expect(shouldSendSecret(false, true, "x")).toBe(false);
  });
  it("sends when editing, without trimming", () => {
    expect(shouldSendSecret(true, true, " pw ")).toBe(true);
  });
});
