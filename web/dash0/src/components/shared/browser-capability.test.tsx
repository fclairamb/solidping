/**
 * @vitest-environment jsdom
 *
 * Component test for the browser capability icon (spec 2026-08-26-01),
 * mirroring the IPv6 capability component it copies its contract from. The
 * negative assertion in "unknown never renders as no" is the specific
 * mistake the spec calls out — assert it explicitly, not just the happy
 * path.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { browserCapability, BrowserCapabilityIcon } from "./browser-capability";

afterEach(cleanup);

describe("browserCapability", () => {
  it("reads yes from the capabilities map", () => {
    expect(browserCapability({ browser: "yes" })).toBe("yes");
  });

  it("reads no from the capabilities map", () => {
    expect(browserCapability({ browser: "no" })).toBe("no");
  });

  it("is unknown for an absent map, NEVER no — an older server may just not send it", () => {
    expect(browserCapability(undefined)).toBe("unknown");
    expect(browserCapability(null)).toBe("unknown");
  });

  it("is unknown when the key is missing from a present map", () => {
    expect(browserCapability({ ipv6: "yes" })).toBe("unknown");
  });

  it("is unknown for any unrecognised value, never guessed as yes or no", () => {
    expect(browserCapability({ browser: "maybe" })).toBe("unknown");
  });
});

describe("BrowserCapabilityIcon", () => {
  it("renders the yes state with the data-browser attribute set", () => {
    render(<BrowserCapabilityIcon capability="yes" data-testid="icon" />);

    const icon = screen.getByTestId("icon");
    expect(icon.getAttribute("data-browser")).toBe("yes");
  });

  it("renders the no state distinctly from yes", () => {
    render(<BrowserCapabilityIcon capability="no" data-testid="icon" />);

    const icon = screen.getByTestId("icon");
    expect(icon.getAttribute("data-browser")).toBe("no");
    expect(icon.className).not.toContain("text-status-ok-foreground");
  });

  it("renders unknown as its own neutral state — never collapsed into no", () => {
    render(<BrowserCapabilityIcon capability="unknown" data-testid="icon" />);

    const icon = screen.getByTestId("icon");
    expect(icon.getAttribute("data-browser")).toBe("unknown");
    expect(icon.className).not.toContain("text-status-warning-foreground");
  });

  it("hides unknown when hideUnknown is set, so a dense surface stays quiet", () => {
    render(<BrowserCapabilityIcon capability="unknown" hideUnknown data-testid="icon" />);

    expect(screen.queryByTestId("icon")).toBeNull();
  });

  it("still renders no even with hideUnknown set — 'no' always renders", () => {
    render(<BrowserCapabilityIcon capability="no" hideUnknown data-testid="icon" />);

    expect(screen.getByTestId("icon")).toBeTruthy();
  });

  it("still renders yes even with hideUnknown set", () => {
    render(<BrowserCapabilityIcon capability="yes" hideUnknown data-testid="icon" />);

    expect(screen.getByTestId("icon")).toBeTruthy();
  });
});
