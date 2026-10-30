/**
 * @vitest-environment jsdom
 *
 * The colleague hint banner (spec 2026-10-30-04): only on the personal org the
 * server names, dismissable for good, and gone once the request is sent.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import "@/i18n";
import { ColleagueHintBanner } from "./colleague-hint-banner";
import { colleagueHintDismissKey } from "@/lib/colleague-hint";

const state = vi.hoisted(() => ({
  hint: null as null | {
    org: { slug: string; name: string } | null;
    domain?: string;
    personalOrgSlug?: string;
  },
  mutateAsync: vi.fn(),
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: { uid: "user-1", isDemo: false } }),
}));

vi.mock("@/api/hooks", () => ({
  useColleagueHint: () => ({ data: state.hint }),
  useCreateMembershipRequest: () => ({
    mutateAsync: state.mutateAsync,
    isPending: false,
  }),
}));

const HINT = {
  org: { slug: "acme", name: "Acme" },
  domain: "acme.com",
  personalOrgSlug: "alice",
};

beforeEach(() => {
  window.localStorage.clear();
  state.hint = HINT;
  state.mutateAsync = vi.fn().mockResolvedValue({});
});

afterEach(() => {
  cleanup();
});

describe("ColleagueHintBanner", () => {
  it("names the colleagues' org on the personal org", () => {
    render(<ColleagueHintBanner org="alice" />);

    const banner = screen.getByTestId("colleague-hint-banner");
    expect(banner.textContent).toContain("acme.com");
    expect(banner.textContent).toContain("Acme");
  });

  it("renders nothing on another org, or without a hint", () => {
    render(<ColleagueHintBanner org="acme" />);
    expect(screen.queryByTestId("colleague-hint-banner")).toBeNull();
    cleanup();

    state.hint = { org: null };
    render(<ColleagueHintBanner org="alice" />);
    expect(screen.queryByTestId("colleague-hint-banner")).toBeNull();
  });

  it("sends the join request and confirms it", async () => {
    render(<ColleagueHintBanner org="alice" />);

    fireEvent.click(screen.getByTestId("colleague-hint-request"));

    await waitFor(() => expect(screen.getByTestId("colleague-hint-sent")).toBeTruthy());
    expect(state.mutateAsync).toHaveBeenCalledWith({ orgSlug: "acme" });
  });

  it("shows an error when the request fails", async () => {
    state.mutateAsync = vi.fn().mockRejectedValue(new Error("nope"));
    render(<ColleagueHintBanner org="alice" />);

    fireEvent.click(screen.getByTestId("colleague-hint-request"));

    await waitFor(() => expect(screen.getByTestId("colleague-hint-error")).toBeTruthy());
  });

  it("stays dismissed", () => {
    render(<ColleagueHintBanner org="alice" />);

    fireEvent.click(screen.getByTestId("colleague-hint-dismiss"));
    expect(screen.queryByTestId("colleague-hint-banner")).toBeNull();
    expect(window.localStorage.getItem(colleagueHintDismissKey("user-1", "acme"))).toBe("1");
    cleanup();

    render(<ColleagueHintBanner org="alice" />);
    expect(screen.queryByTestId("colleague-hint-banner")).toBeNull();
  });
});
