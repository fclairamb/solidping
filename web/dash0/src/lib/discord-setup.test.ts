import { describe, expect, it } from "vitest";
import { showLocalhostWarning } from "./discord-setup";

describe("showLocalhostWarning", () => {
  it("warns on the default base URL reached through another host", () => {
    expect(showLocalhostWarning(true, "status.acme.com")).toBe(true);
  });
  it("stays quiet when the dashboard is on localhost", () => {
    expect(showLocalhostWarning(true, "localhost")).toBe(false);
    expect(showLocalhostWarning(true, "127.0.0.1")).toBe(false);
  });
  it("stays quiet once the base URL is configured", () => {
    expect(showLocalhostWarning(false, "status.acme.com")).toBe(false);
  });
});
