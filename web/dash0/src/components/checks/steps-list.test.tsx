/**
 * @vitest-environment jsdom
 *
 * Spec 2026-11-17-04: the steps of a script run, as a list.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";

import { parseSteps } from "@/lib/check-steps";
import { StepsList } from "./steps-list";

afterEach(cleanup);

describe("parseSteps", () => {
  it("reads the steps and drops malformed entries", () => {
    const steps = parseSteps({
      steps: [
        { name: "Open", status: "ok", durationMs: 12 },
        { status: "ok" },
        { name: "Click", status: "failed", durationMs: 3, error: "nope" },
      ],
    });
    expect(steps.map((s) => s.name)).toEqual(["Open", "Click"]);
    expect(steps[1].error).toBe("nope");
  });

  it("returns nothing for a result without steps", () => {
    expect(parseSteps({ error: "x" })).toEqual([]);
    expect(parseSteps(undefined)).toEqual([]);
  });
});

describe("StepsList", () => {
  it("renders ok, failed and skipped items with their error", () => {
    render(
      <StepsList
        steps={[
          { name: "Open", status: "ok", durationMs: 5 },
          { name: "Click", status: "failed", durationMs: 2, error: "boom" },
          { name: "Check", status: "skipped", durationMs: 0 },
        ]}
      />,
    );
    const items = screen.getAllByTestId("check-step-item");
    expect(items.map((i) => i.getAttribute("data-status"))).toEqual(["ok", "failed", "skipped"]);
    expect(screen.getByTestId("check-step-error").textContent).toBe("boom");
  });
});
