/**
 * @vitest-environment jsdom
 *
 * Spec 2026-10-30-04: where sign-ins create a personal org, "I'd rather join
 * an existing organization" is only offered to a user who came through a join
 * path. Without auth.auto_personal_org it is always offered.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { JoinOrgCard } from "./join-org-card";

const requests = vi.hoisted(() => ({ data: undefined as unknown }));
vi.mock("@/api/hooks", () => ({
  useCreateMembershipRequest: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useMyMembershipRequests: () => ({ data: requests.data }),
}));

vi.mock("@/api/client", () => ({ ApiError: class extends Error {} }));

const publicConfig = vi.hoisted(() => ({ autoPersonalOrg: true as boolean | undefined }));
vi.mock("@/api/public-config", () => ({
  useAutoPersonalOrg: () => publicConfig.autoPersonalOrg,
}));

afterEach(() => {
  cleanup();
  requests.data = undefined;
  publicConfig.autoPersonalOrg = true;
});

describe("JoinOrgCard", () => {
  it("is hidden without a join reason", () => {
    render(<JoinOrgCard />);
    expect(screen.queryByTestId("no-org-join-toggle")).toBeNull();
  });

  it("is hidden when the only request is approved or cancelled", () => {
    requests.data = { data: [{ uid: "1", status: "approved" }] };
    render(<JoinOrgCard />);
    expect(screen.queryByTestId("no-org-join-toggle")).toBeNull();
  });

  it("is shown with ?join=<slug>", () => {
    render(<JoinOrgCard joinSlug="acme" />);
    expect(screen.getByTestId("no-org-join-toggle")).toBeTruthy();
  });

  it("is shown after a sign-in the org did not admit", () => {
    render(<JoinOrgCard membershipPending="acme" />);
    expect(screen.getByTestId("no-org-join-toggle")).toBeTruthy();
  });

  it.each(["pending", "rejected"])("is shown with a %s membership request", (status) => {
    requests.data = { data: [{ uid: "1", status }] };
    render(<JoinOrgCard />);
    expect(screen.getByTestId("no-org-join-toggle")).toBeTruthy();
  });

  it("is hidden while the public config loads", () => {
    publicConfig.autoPersonalOrg = undefined;
    render(<JoinOrgCard />);
    expect(screen.queryByTestId("no-org-join-toggle")).toBeNull();
  });

  it("is always shown when sign-ins do not create a personal org", () => {
    publicConfig.autoPersonalOrg = false;
    render(<JoinOrgCard />);
    expect(screen.getByTestId("no-org-join-toggle")).toBeTruthy();
  });
});
