/**
 * @vitest-environment jsdom
 *
 * Component test for spec 2026-08-28-05: CheckMultiPicker chips must resolve
 * a name for a selected check outside the current search page, and once
 * resolved that name must never regress — not even to the "…" loading
 * placeholder, let alone the raw UUID — when a later, narrower search
 * excludes the check from the live result set.
 *
 * The data-fetching hooks (useChecks, useCheckGroups, useChecksByUids) are
 * mocked directly rather than stubbing `fetch`: this test is about the
 * component's own label-resolution and stickiness logic, not the network
 * layer, and mocking the hooks lets a "search narrowed" transition be driven
 * deterministically via `rerender` instead of interacting with the Radix
 * Popover (covered instead by the Playwright e2e in this same spec).
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { CheckMultiPicker } from "./check-multi-picker";
import * as hooks from "@/api/hooks";
import type { Check } from "@/api/hooks";

vi.mock("@/api/hooks", async () => {
  const actual = await vi.importActual<typeof import("@/api/hooks")>("@/api/hooks");
  return {
    ...actual,
    useChecks: vi.fn(),
    useCheckGroups: vi.fn(),
    useChecksByUids: vi.fn(),
  };
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// Minimal stand-ins for the react-query result shapes the component reads
// (`.data`, `.isError`) — the fields it doesn't touch are irrelevant here.
function mockChecks(checks: Check[]) {
  vi.mocked(hooks.useChecks).mockReturnValue({ data: checks } as never);
}
function mockGroups() {
  vi.mocked(hooks.useCheckGroups).mockReturnValue({ data: [] } as never);
}
function mockByUids(results: Array<{ data?: Check; isError?: boolean }>) {
  vi.mocked(hooks.useChecksByUids).mockReturnValue(results as never);
}

const UUID_PATTERN = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

describe("CheckMultiPicker chip labels", () => {
  it("resolves the name of a selected check outside the current search page", () => {
    const uid = "57c34008-6f53-4794-b2fa-222222222222";
    mockChecks([]); // current search page does not include the selected check
    mockGroups();
    // Individual resolution still pending.
    mockByUids([{ data: undefined, isError: false }]);

    const { rerender } = render(
      <CheckMultiPicker
        org="acme"
        kind="checks"
        value={[uid]}
        onChange={() => {}}
        data-testid="picker"
      />,
    );

    // While resolving, a placeholder is shown — never the raw uid.
    expect(screen.getByTestId(`picker-chip-remove-${uid}`)).toBeTruthy();
    expect(screen.queryByText(UUID_PATTERN)).toBeNull();
    expect(screen.getByText("…")).toBeTruthy();

    // The individual fetch resolves with a name.
    mockByUids([{ data: { uid, name: "acme-primary-db" }, isError: false }]);
    rerender(
      <CheckMultiPicker
        org="acme"
        kind="checks"
        value={[uid]}
        onChange={() => {}}
        data-testid="picker"
      />,
    );

    expect(screen.getByText("acme-primary-db")).toBeTruthy();
    expect(screen.queryByText(UUID_PATTERN)).toBeNull();
  });

  it("falls back to the raw uid when the individual fetch errors (deleted check)", () => {
    const uid = "deadbeef-0000-4000-8000-000000000001";
    mockChecks([]);
    mockGroups();
    mockByUids([{ data: undefined, isError: true }]);

    render(
      <CheckMultiPicker org="acme" kind="checks" value={[uid]} onChange={() => {}} />,
    );

    expect(screen.getByText(uid)).toBeTruthy();
  });

  it("keeps a resolved name sticky across a narrower search that drops the check from results", () => {
    const uid = "11111111-2222-4333-8444-555555555555";
    // First render: the check is present in the current search page, so its
    // label resolves for free from `checkMatches` — no individual fetch
    // needed yet.
    mockChecks([{ uid, name: "acme-edge-worker" }]);
    mockGroups();
    mockByUids([]);

    const { rerender } = render(
      <CheckMultiPicker org="acme" kind="checks" value={[uid]} onChange={() => {}} />,
    );

    expect(screen.getByText("acme-edge-worker")).toBeTruthy();

    // The user narrows their search: the check drops out of the current
    // page, and its individual resolution has not come back yet (still
    // pending in this render).
    mockChecks([]);
    mockByUids([{ data: undefined, isError: false }]);

    rerender(
      <CheckMultiPicker org="acme" kind="checks" value={[uid]} onChange={() => {}} />,
    );

    // The chip must still show the name — not the "…" placeholder and
    // certainly not the uid — because it was already resolved once.
    expect(screen.getByText("acme-edge-worker")).toBeTruthy();
    expect(screen.queryByText("…")).toBeNull();
    expect(screen.queryByText(UUID_PATTERN)).toBeNull();
  });

  it("does not fetch individual checks for kind=groups", () => {
    const uid = "group-uid-1";
    mockChecks([]);
    mockGroups();
    mockByUids([]); // no per-uid fetches expected for groups

    render(
      <CheckMultiPicker
        org="acme"
        kind="groups"
        value={[uid]}
        onChange={() => {}}
      />,
    );

    // Groups are fetched whole; an unresolved group uid falls back to the
    // raw uid itself rather than showing a "…" loading state — there is
    // nothing left to resolve.
    expect(screen.getByText(uid)).toBeTruthy();
    expect(vi.mocked(hooks.useChecksByUids)).toHaveBeenCalledWith("acme", []);
  });
});
