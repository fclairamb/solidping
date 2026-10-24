import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  PENDING_CHECK_TTL_MS,
  consumePendingCheck,
  filterMonitorQuery,
  isMonitorReturnTo,
  landingForOrg,
  monitorReturnTo,
  savePendingCheck,
} from "./pending-check";

beforeEach(() => {
  const store = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  });
});

describe("filterMonitorQuery", () => {
  it("drops unknown keys and non-scalar values", () => {
    expect(
      filterMonitorQuery({
        checkType: "http",
        httpUrl: "https://example.com",
        evil: "x",
        returnTo: "//attacker",
        port: { a: 1 },
        region: ["us1", "eu2"],
      }),
    ).toEqual({
      checkType: "http",
      httpUrl: "https://example.com",
      region: ["us1", "eu2"],
    });
  });
});

describe("pending check", () => {
  it("is consumed once", () => {
    savePendingCheck({ checkType: "http" }, 1000);
    expect(consumePendingCheck(2000)).toEqual({ checkType: "http" });
    expect(consumePendingCheck(2000)).toBeNull();
  });

  it("expires after 24h", () => {
    savePendingCheck({ checkType: "http" }, 1000);
    expect(consumePendingCheck(1000 + PENDING_CHECK_TTL_MS)).toBeNull();
    expect(localStorage.getItem("solidping_pending_check")).toBeNull();
  });

  it("filters tampered storage", () => {
    localStorage.setItem(
      "solidping_pending_check",
      JSON.stringify({ query: { bad: "1" }, expiresAt: Date.now() + 10000 }),
    );
    expect(consumePendingCheck()).toBeNull();
  });
});

describe("landingForOrg", () => {
  it("lands on the prefilled form, once", () => {
    savePendingCheck({ checkType: "http", httpUrl: "https://example.com" });
    expect(landingForOrg("acme")).toEqual({
      to: "/orgs/$org/checks/new",
      params: { org: "acme" },
      search: { checkType: "http", httpUrl: "https://example.com" },
    });
    expect(landingForOrg("acme").to).toBe("/orgs/$org");
  });

  it("never gives the demo org the prefill", () => {
    savePendingCheck({ checkType: "http" });
    expect(landingForOrg("demo", true).to).toBe("/orgs/$org");
  });
});

describe("monitor returnTo", () => {
  it("round-trips and is path-anchored", () => {
    const rt = monitorReturnTo("/d", { checkType: "http", region: ["a", "b"] });
    expect(rt).toBe("/d/monitor?checkType=http&region=a&region=b");
    expect(isMonitorReturnTo(rt, "/d")).toBe(true);
    expect(isMonitorReturnTo("/d/monitorx", "/d")).toBe(false);
    expect(isMonitorReturnTo("//evil/d/monitor", "/d")).toBe(false);
  });
});
