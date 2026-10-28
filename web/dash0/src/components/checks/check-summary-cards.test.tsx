import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type { Check } from "@/api/hooks";
import { CheckSummaryCards } from "./check-summary-cards";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

function statusLabel(status: string): string {
  const check = {
    uid: "c1",
    status,
    statusChangedAt: new Date(Date.now() - 16_000).toISOString(),
  } as unknown as Check;
  const html = renderToStaticMarkup(
    <CheckSummaryCards org="acme" check={check} totalIncidents={0} />,
  );

  return /data-testid="summary-status-label"[^>]*>(.*?)<\/div>/s.exec(html)?.[1] ?? "";
}

// solidping-business#26: a validating check showed a bare "16s" under the
// generic "Status" label, without saying what the duration measures.
describe("CheckSummaryCards status label", () => {
  it("says 'Validating for' while the check is validating", () => {
    const label = statusLabel("validating");
    expect(label).toContain("detail.summary.validatingFor");
    expect(label).not.toContain("detail.summary.statusFallback");
  });

  it("keeps the up, down and stale labels", () => {
    expect(statusLabel("up")).toContain("detail.summary.currentlyUp");
    expect(statusLabel("down")).toContain("detail.summary.currentlyDown");
    expect(statusLabel("stale")).toContain("detail.summary.noDataFor");
  });
});
