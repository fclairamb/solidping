import { describe, expect, it } from "vitest";
import { ApiError, NetworkError } from "@/api/client";
import { isInviteInvalidError } from "./invite-error";

describe("isInviteInvalidError", () => {
  it("treats a 404 INVITATION_NOT_FOUND as invalid", () => {
    const err = new ApiError("Invitation not found", "INVITATION_NOT_FOUND", undefined, 404);
    expect(isInviteInvalidError(err)).toBe(true);
  });

  it("treats a 410 INVITATION_EXPIRED as invalid", () => {
    const err = new ApiError("Invitation has expired", "INVITATION_EXPIRED", undefined, 410);
    expect(isInviteInvalidError(err)).toBe(true);
  });

  it("treats a code-only match as invalid even with an unexpected status", () => {
    // Belt-and-suspenders: if the status is ever proxied/rewritten, the code
    // alone should still be enough to recognize a genuine dead link.
    const err = new ApiError("Invitation not found", "INVITATION_NOT_FOUND", undefined, undefined);
    expect(isInviteInvalidError(err)).toBe(true);
  });

  it("treats a 429 rate limit as retryable, not invalid", () => {
    const err = new ApiError("Too many requests", "RATE_LIMITED", undefined, 429);
    expect(isInviteInvalidError(err)).toBe(false);
  });

  it("treats a 500 as retryable, not invalid", () => {
    const err = new ApiError("Internal error", "INTERNAL_ERROR", undefined, 500);
    expect(isInviteInvalidError(err)).toBe(false);
  });

  it("treats a network error as retryable, not invalid", () => {
    expect(isInviteInvalidError(new NetworkError())).toBe(false);
  });

  it("treats an arbitrary thrown error as retryable, not invalid", () => {
    expect(isInviteInvalidError(new Error("boom"))).toBe(false);
    expect(isInviteInvalidError("boom")).toBe(false);
    expect(isInviteInvalidError(undefined)).toBe(false);
  });
});
