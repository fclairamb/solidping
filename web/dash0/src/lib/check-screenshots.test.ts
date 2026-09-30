import { describe, expect, it } from "vitest";

import { checkTypeCanCapture, pendingCaptureFailure } from "./check-screenshots";

describe("checkTypeCanCapture", () => {
  it("is true for the types that can capture", () => {
    expect(checkTypeCanCapture("browser")).toBe(true);
    expect(checkTypeCanCapture("js")).toBe(true);
    expect(checkTypeCanCapture("rdp")).toBe(true);
    expect(checkTypeCanCapture("vnc")).toBe(true);
  });

  it("is false for every other type, and for a missing one", () => {
    expect(checkTypeCanCapture("http")).toBe(false);
    expect(checkTypeCanCapture("ssh")).toBe(false);
    expect(checkTypeCanCapture(undefined)).toBe(false);
  });
});

describe("pendingCaptureFailure", () => {
  const requestedAt = "2026-09-26T22:50:31.123456789Z";

  it("returns the outcome when it reports this request as failed", () => {
    const outcome = {
      requestedAt: "2026-09-26T22:50:31.123456Z",
      failed: true,
      error: "the capture timed out after 5s",
    };
    expect(pendingCaptureFailure(requestedAt, outcome)).toEqual(outcome);
  });

  it("is null while no outcome is reported (the run has not finished)", () => {
    expect(pendingCaptureFailure(requestedAt, undefined)).toBeNull();
  });

  it("ignores the failure of an earlier request", () => {
    expect(
      pendingCaptureFailure(requestedAt, {
        requestedAt: "2026-09-26T22:48:02.000001Z",
        failed: true,
        error: "older",
      }),
    ).toBeNull();
  });

  it("ignores an outcome that is not a failure", () => {
    expect(
      pendingCaptureFailure(requestedAt, { requestedAt: "2026-09-26T22:50:31.123456Z", failed: false }),
    ).toBeNull();
  });

  it("ignores unparseable timestamps", () => {
    expect(pendingCaptureFailure("nope", { requestedAt: "also nope", failed: true })).toBeNull();
  });
});
