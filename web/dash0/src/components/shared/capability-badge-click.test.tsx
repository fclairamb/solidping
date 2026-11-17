/**
 * @vitest-environment jsdom
 *
 * The region picker wraps each region row in a <label>, capability badges
 * included. A click on a badge must open its explanation, never bubble to the
 * label and toggle the region's checkbox.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Checkbox } from "@/components/ui/checkbox";
import { Ipv6CapabilityBadge } from "./ipv6-capability";
import { BrowserCapabilityIcon } from "./browser-capability";

afterEach(cleanup);

// Radix measures its popper content; jsdom has no ResizeObserver.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

function renderRow(onToggle: () => void) {
  render(
    <label data-testid="region-option-acme">
      <Checkbox onCheckedChange={onToggle} data-testid="region-checkbox-acme" />
      <span>Acme Region</span>
      <Ipv6CapabilityBadge capability="yes" data-testid="region-ipv6-acme" />
      <BrowserCapabilityIcon capability="yes" data-testid="region-browser-acme" />
    </label>,
  );
}

describe("capability badges inside a region row", () => {
  it("a click on the IPv6 badge shows its hint and leaves the region alone", () => {
    const onToggle = vi.fn();
    renderRow(onToggle);

    const badge = screen.getByTestId("region-ipv6-acme");
    fireEvent.click(badge);
    fireEvent.click(badge);

    expect(onToggle).not.toHaveBeenCalled();
    expect(badge.getAttribute("data-state")).toMatch(/open/);
  });

  it("a click on the browser icon leaves the region alone", () => {
    const onToggle = vi.fn();
    renderRow(onToggle);

    fireEvent.click(screen.getByTestId("region-browser-acme"));

    expect(onToggle).not.toHaveBeenCalled();
  });

  it("a click on the checkbox still toggles the region", () => {
    const onToggle = vi.fn();
    renderRow(onToggle);

    fireEvent.click(screen.getByTestId("region-checkbox-acme"));

    expect(onToggle).toHaveBeenCalledTimes(1);
  });
});
