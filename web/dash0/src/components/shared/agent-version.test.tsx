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
import { AgentVersionCell, agentVersionState, isAgentOutdated, isAgentUnsupported } from "./agent-version";

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

  it("renders Unsupported instead of Drifted below the minimum (spec 2026-11-26-04)", () => {
    render(
      <AgentVersionCell
        agentVersion="0.1.0"
        serverVersion="0.17.0"
        supported={false}
        data-testid="v"
      />
    );

    expect(screen.getByTestId("v-unsupported").textContent).toBe("Unsupported");
    expect(screen.queryByText("Drifted")).toBeNull();
  });

  it("renders Drifted, not Unsupported, above the minimum", () => {
    render(<AgentVersionCell agentVersion="0.16.2" serverVersion="0.17.0" supported data-testid="v" />);

    expect(screen.getByText("Drifted")).toBeTruthy();
    expect(screen.queryByTestId("v-unsupported")).toBeNull();
  });

  it("renders null as unknown even when flagged unsupported", () => {
    render(<AgentVersionCell agentVersion={null} serverVersion="0.17.0" supported={false} data-testid="v" />);

    expect(screen.getByTestId("v").getAttribute("data-agent-version")).toBe("unknown");
    expect(screen.queryByTestId("v-unsupported")).toBeNull();
  });

  it("renders a pinned drifted agent with Drifted plus Pinned (spec 2026-11-22-04)", () => {
    render(
      <AgentVersionCell agentVersion="0.16.2" serverVersion="0.17.0" pinned data-testid="v" />
    );

    const cell = screen.getByTestId("v");
    expect(cell.getAttribute("data-agent-version")).toBe("drifted");
    expect(screen.getByText("Drifted")).toBeTruthy();
    expect(screen.getByTestId("v-pinned").textContent).toBe("Pinned");
  });

  it("shows no Pinned label on an unpinned drifted agent, nor on a pinned matching one", () => {
    render(<AgentVersionCell agentVersion="0.16.2" serverVersion="0.17.0" data-testid="v" />);
    expect(screen.queryByTestId("v-pinned")).toBeNull();
    cleanup();

    render(
      <AgentVersionCell agentVersion="0.17.0" serverVersion="0.17.0" pinned data-testid="v" />
    );
    expect(screen.queryByTestId("v-pinned")).toBeNull();
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

describe("isAgentOutdated", () => {
  it("flags an active, drifted, unpinned agent", () => {
    expect(isAgentOutdated({ status: "active", version: "0.49.3" }, "0.55.0")).toBe(true);
  });

  it("never flags matching, unknown, pinned or revoked agents", () => {
    expect(isAgentOutdated({ status: "active", version: "0.55.0" }, "0.55.0")).toBe(false);
    expect(isAgentOutdated({ status: "active", version: null }, "0.55.0")).toBe(false);
    expect(isAgentOutdated({ status: "active", version: "0.49.3" }, undefined)).toBe(false);
    expect(
      isAgentOutdated({ status: "active", version: "0.49.3", versionPinned: true }, "0.55.0")
    ).toBe(false);
    expect(isAgentOutdated({ status: "revoked", version: "0.49.3" }, "0.55.0")).toBe(false);
  });
});

describe("isAgentUnsupported", () => {
  it("flags an active agent below the supported window", () => {
    expect(isAgentUnsupported({ status: "active", versionSupported: false })).toBe(true);
  });

  it("does not flag supported, unknown or revoked agents", () => {
    expect(isAgentUnsupported({ status: "active", versionSupported: true })).toBe(false);
    expect(isAgentUnsupported({ status: "active" })).toBe(false);
    expect(isAgentUnsupported({ status: "revoked", versionSupported: false })).toBe(false);
  });
});
