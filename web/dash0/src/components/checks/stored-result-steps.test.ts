import { describe, expect, it } from "vitest";

import type { CheckScreenshot } from "@/api/hooks";
import { matchCapture } from "./stored-result-steps";

const shot = (uid: string, capturedAt: string) =>
  ({ uid, capturedAt, downloadUrl: `/pub/files/${uid}` }) as CheckScreenshot;

describe("matchCapture", () => {
  const ts = "2026-11-17T10:00:00Z";

  it("returns the capture taken during the result's run", () => {
    const got = matchCapture(
      [shot("new", "2026-11-17T10:05:00Z"), shot("mine", "2026-11-17T10:00:02Z")],
      ts,
      3000,
    );
    expect(got?.uid).toBe("mine");
  });

  it("returns nothing when only another run's capture exists", () => {
    expect(matchCapture([shot("other", "2026-11-17T10:05:00Z")], ts, 3000)).toBeUndefined();
    expect(matchCapture([shot("x", ts)], undefined, 3000)).toBeUndefined();
  });
});
