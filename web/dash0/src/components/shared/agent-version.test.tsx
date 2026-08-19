/**
 * @vitest-environment jsdom
 *
 * Component test for the agent build-version comparison (spec
 * 2026-08-19-07). The negative assertion in "unknown, never drifted" is the
 * specific mistake the spec calls out — assert it explicitly, not just the
 * happy path.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { AgentVersionCell, agentVersionState } from "./agent-version";

afterEach(cleanup);

describe("agentVersionState", () => {
  it("is match when both sides report the same version", () => {
    expect(agentVersionState("0.17.0", "0.17.0")).toBe("match");
  });

  it("is drifted when the versions genuinely differ", () => {
    expect(agentVersionState("0.16.2", "0.17.0")).toBe("drifted");
  });

  it("is unknown, NEVER drifted, when the agent has never reported (null)", () => {
    expect(agentVersionState(null, "0.17.0")).toBe("unknown");
    expect(agentVersionState(undefined, "0.17.0")).toBe("unknown");
  });

  it("is unknown when the server's own version has not loaded yet", () => {
    expect(agentVersionState("0.17.0", undefined)).toBe("unknown");
  });

  it("is unknown when neither side is known", () => {
    expect(agentVersionState(null, null)).toBe("unknown");
  });
});

describe("AgentVersionCell", () => {
  it("renders a matching version as plain text, no badge", () => {
    render(<AgentVersionCell agentVersion="0.17.0" serverVersion="0.17.0" data-testid="v" />);

    const cell = screen.getByTestId("v");
    expect(cell.getAttribute("data-agent-version")).toBe("match");
    expect(cell.textContent).toBe("0.17.0");
    expect(screen.queryByText("Drifted")).toBeNull();
  });

  it("renders a genuine mismatch with the Drifted badge", () => {
    render(<AgentVersionCell agentVersion="0.16.2" serverVersion="0.17.0" data-testid="v" />);

    const cell = screen.getByTestId("v");
    expect(cell.getAttribute("data-agent-version")).toBe("drifted");
    expect(cell.textContent).toContain("0.16.2");
    expect(screen.getByText("Drifted")).toBeTruthy();
  });

  it("renders null as unknown, NEVER as drifted — the regression this spec calls out", () => {
    render(<AgentVersionCell agentVersion={null} serverVersion="0.17.0" data-testid="v" />);

    const cell = screen.getByTestId("v");
    expect(cell.getAttribute("data-agent-version")).toBe("unknown");
    expect(cell.textContent).toBe("unknown");
    expect(screen.queryByText("Drifted")).toBeNull();
  });

  it("never uses destructive red for a drift warning — it is advisory, not an error", () => {
    render(<AgentVersionCell agentVersion="0.16.2" serverVersion="0.17.0" data-testid="v" />);

    const badge = screen.getByText("Drifted");
    expect(badge.className).not.toContain("destructive");
  });
});
