import { describe, expect, it } from "vitest";
import { shouldShowJoinCard } from "./no-org-join";

describe("shouldShowJoinCard", () => {
  it("is hidden without a join reason", () => {
    expect(shouldShowJoinCard({})).toBe(false);
    expect(shouldShowJoinCard({ requests: [] })).toBe(false);
    expect(
      shouldShowJoinCard({ requests: [{ status: "approved" }, { status: "cancelled" }] }),
    ).toBe(false);
  });

  it("is shown with ?join=<slug>", () => {
    expect(shouldShowJoinCard({ join: "acme" })).toBe(true);
  });

  it("is shown after a refused sign-in", () => {
    expect(shouldShowJoinCard({ membershipPending: "acme" })).toBe(true);
  });

  it("is shown with a pending or rejected request", () => {
    expect(shouldShowJoinCard({ requests: [{ status: "pending" }] })).toBe(true);
    expect(shouldShowJoinCard({ requests: [{ status: "rejected" }] })).toBe(true);
  });
});
