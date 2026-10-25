/**
 * @vitest-environment jsdom
 *
 * Component test for the persistent region warning of a plugin check (spec
 * 2026-10-17-05 §17).
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { RegionWarningsAlert } from "./region-warnings-alert";

afterEach(cleanup);

describe("RegionWarningsAlert", () => {
  it("renders nothing without warnings", () => {
    render(<RegionWarningsAlert />);
    expect(screen.queryByTestId("region-warnings")).toBeNull();

    render(<RegionWarningsAlert warnings={[]} />);
    expect(screen.queryByTestId("region-warnings")).toBeNull();
  });

  it("shows each reason the server gave", () => {
    render(
      <RegionWarningsAlert
        warnings={[
          {
            name: "regions",
            code: "REGION_NO_PLUGIN",
            message: "no live worker in 🇪🇺 Europe has the x-gopher plugin",
          },
        ]}
      />,
    );

    const alert = screen.getByTestId("region-warnings");
    expect(alert.textContent).toContain("does not run in every region");
    expect(alert.textContent).not.toContain("checks:detail");
    expect(screen.getAllByTestId("region-warning-item")).toHaveLength(1);
    expect(alert.textContent).toContain("x-gopher plugin");
    expect(alert.className).not.toContain("destructive");
  });
});
