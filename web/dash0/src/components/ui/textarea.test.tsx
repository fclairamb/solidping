import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Textarea } from "./textarea";
import { NO_CAPTURE_CLASS } from "@/lib/no-capture";

// Spec 2026-11-17-08: dash0 records session replays unmasked, so a textarea
// that takes a pasted document (the check import payload) opts out with
// `data-secret-input`, which adds `ph-no-capture`.

describe("Textarea", () => {
  it("adds ph-no-capture with data-secret-input", () => {
    const html = renderToStaticMarkup(<Textarea data-secret-input data-testid="t" />);
    expect(html).toMatch(/class="[^"]*\bph-no-capture\b/);
  });

  it("stays capturable without data-secret-input", () => {
    const html = renderToStaticMarkup(<Textarea data-testid="t" />);
    expect(html).not.toContain(NO_CAPTURE_CLASS);
  });
});
