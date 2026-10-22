import { describe, expect, it } from "vitest";
import { upgradeView } from "@/lib/upgrade-redirect";

describe("upgradeView", () => {
  it("redirects an admin to the signed upgradeUrl", () => {
    expect(
      upgradeView({ isLoading: false, isError: false, upgradeUrl: "https://billing.acme.com/up#bt=x" }),
    ).toEqual({ kind: "redirect", url: "https://billing.acme.com/up#bt=x" });
  });

  it("shows a message, no redirect, when the viewer gets no upgradeUrl", () => {
    expect(upgradeView({ isLoading: false, isError: false })).toEqual({ kind: "unavailable" });
  });

  it("shows a message when the request fails", () => {
    expect(upgradeView({ isLoading: false, isError: true })).toEqual({ kind: "unavailable" });
  });

  it("waits while loading", () => {
    expect(upgradeView({ isLoading: true, isError: false })).toEqual({ kind: "loading" });
  });
});
