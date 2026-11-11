import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { SecretText } from "./secret-text";
import { NO_CAPTURE_CLASS } from "@/lib/no-capture";

// Spec 2026-11-11-05: posthog-js blocks `ph-no-capture` from session replay
// and autocapture. The primitive must always carry it, whatever the caller
// passes as className.

describe("SecretText", () => {
  it("renders a <code> with ph-no-capture and data-secret-text", () => {
    const html = renderToStaticMarkup(
      <SecretText className="text-xs" data-testid="s">
        AGE-SECRET-KEY-1ABC
      </SecretText>,
    );
    expect(NO_CAPTURE_CLASS).toBe("ph-no-capture");
    expect(html).toMatch(/^<code /);
    expect(html).toContain('data-secret-text=""');
    expect(html).toMatch(/class="ph-no-capture text-xs"/);
    expect(html).toContain('data-testid="s"');
    expect(html).toContain("AGE-SECRET-KEY-1ABC");
  });

  it("renders a <pre> when asked, still blocked", () => {
    const html = renderToStaticMarkup(<SecretText as="pre">SP_AGENT_REGION_KEYS=x</SecretText>);
    expect(html).toMatch(/^<pre /);
    expect(html).toContain('class="ph-no-capture"');
    expect(html).toContain("data-secret-text");
  });
});
