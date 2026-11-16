/**
 * @vitest-environment jsdom
 *
 * The incident insights period selector must be the dashboard's
 * SegmentedControl (one recessed track, the selected segment highlighted),
 * not a row of loose outline buttons.
 */
import type { ComponentType } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import "@/i18n";

const navigate = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: Record<string, unknown>) => ({
    options,
    useParams: () => ({ org: "acme" }),
    useSearch: () => ({ period: "7d" }),
  }),
  useNavigate: () => navigate,
  Link: () => null,
}));

vi.mock("@/api/hooks", () => ({
  useIncidentInsights: () => ({
    data: undefined,
    isLoading: true,
    error: null,
    refetch: vi.fn(),
  }),
}));

import { Route } from "./incidents.insights";

const Page = (Route as unknown as { options: { component: ComponentType } })
  .options.component;

afterEach(() => {
  cleanup();
  navigate.mockReset();
});

describe("incident insights period selector", () => {
  it("is a segmented control, like the dashboard window selector", () => {
    render(<Page />);

    const group = screen.getByTestId("insights-period-selector");
    expect(group.getAttribute("role")).toBe("group");
    expect(group.getAttribute("aria-label")).toBe("Period");
    expect(group.className).toContain("bg-muted");

    expect(
      screen.getByTestId("insights-period-7d").getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen.getByTestId("insights-period-30d").getAttribute("aria-pressed"),
    ).toBe("false");
  });

  it("navigates to the chosen period", () => {
    render(<Page />);

    fireEvent.click(screen.getByTestId("insights-period-90d"));
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({ search: { period: "90d" } }),
    );
  });
});
