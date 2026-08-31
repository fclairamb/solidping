/**
 * @vitest-environment jsdom
 *
 * Component test for the claimed-elsewhere explanation (spec 2026-08-31-01).
 * Mirrors needs-reseal-alert.test.tsx: a real report came in read as a broken
 * label rule when the actual cause was every match being claimed by an
 * earlier section, so this pins that the hint tells the two apart and says
 * nothing when there is nothing to explain.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { SelectorClaimedElsewhereAlert } from "./selector-claimed-elsewhere-alert";

afterEach(cleanup);

describe("SelectorClaimedElsewhereAlert", () => {
  it("renders nothing when nothing is claimed elsewhere", () => {
    render(<SelectorClaimedElsewhereAlert ownResourceCount={3} />);
    expect(
      screen.queryByTestId("section-selector-claimed-elsewhere"),
    ).toBeNull();
  });

  it("renders nothing when claimedElsewhere is explicitly zero", () => {
    render(
      <SelectorClaimedElsewhereAlert
        ownResourceCount={3}
        claimedElsewhere={0}
      />,
    );
    expect(
      screen.queryByTestId("section-selector-claimed-elsewhere"),
    ).toBeNull();
  });

  it("gives the fuller explanation, naming the claimant, when the section is empty", () => {
    render(
      <SelectorClaimedElsewhereAlert
        ownResourceCount={0}
        claimedElsewhere={2}
        claimantName="All services"
      />,
    );

    const alert = screen.getByTestId("section-selector-claimed-elsewhere");
    expect(alert).toBeTruthy();

    // Real copy, not a raw i18n key.
    expect(alert.textContent).not.toContain("statusPages:sections");
    expect(alert.textContent).toContain("2");
    expect(alert.textContent).toContain("All services");
    // The full explanation names the actual remedy.
    expect(alert.textContent).toContain("move this section higher");
  });

  it("gives the lighter, count-only hint when the section still shows some of its own matches", () => {
    render(
      <SelectorClaimedElsewhereAlert
        ownResourceCount={2}
        claimedElsewhere={1}
        claimantName="All services"
      />,
    );

    const alert = screen.getByTestId("section-selector-claimed-elsewhere");
    expect(alert.textContent).toContain("1");
    expect(alert.textContent).toContain("elsewhere on this page");
    // The partial copy deliberately drops the claimant's name.
    expect(alert.textContent).not.toContain("All services");
  });

  it("is a warning, never a destructive state", () => {
    render(
      <SelectorClaimedElsewhereAlert ownResourceCount={0} claimedElsewhere={2} />,
    );

    const alert = screen.getByTestId("section-selector-claimed-elsewhere");
    expect(alert.className).not.toContain("destructive");
  });
});
