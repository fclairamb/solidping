/**
 * @vitest-environment jsdom
 *
 * The /organization/* layout sends non-admins back to the org dashboard. It
 * used to call navigate() during render, which loops the router until React
 * aborts with "Maximum update depth exceeded" (error #185).
 */
import type { ComponentType } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import "@/i18n";

const navigate = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: Record<string, unknown>) => ({
    options,
    useParams: () => ({ org: "acme" }),
  }),
  Outlet: () => <div data-testid="org-outlet" />,
  useMatchRoute: () => () => false,
  useNavigate: () => navigate,
  Link: () => null,
}));

const mockUser = vi.hoisted(() => ({
  current: { isAdmin: false } as { isAdmin: boolean },
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: mockUser.current, isLoading: false }),
}));

vi.mock("@/api/hooks", () => ({
  useOrgMembershipRequests: () => ({ data: undefined }),
}));

vi.mock("@/api/client", () => ({ isOrgDeleted: () => false }));

vi.mock("@/components/shared/tab-nav", () => ({ TabNav: () => null }));
vi.mock("@/components/shared/page-header", () => ({ PageHeader: () => null }));
vi.mock("@/components/shared/create-invitation-dialog", () => ({
  CreateInvitationDialog: () => null,
}));

import { Route } from "./organization";

const Layout = (Route as unknown as { options: { component: ComponentType } }).options.component;

beforeEach(() => {
  navigate.mockReset();
  mockUser.current = { isAdmin: false };
});

afterEach(() => cleanup());

describe("organization layout admin guard", () => {
  it("never navigates during render", () => {
    // Server rendering runs the render phase only, no effects: any navigate()
    // seen here was called while rendering.
    renderToStaticMarkup(<Layout />);
    expect(navigate).not.toHaveBeenCalled();
  });

  it("redirects a non-admin to the org dashboard once mounted", async () => {
    const { queryByTestId } = render(<Layout />);
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: "/orgs/$org",
        params: { org: "acme" },
        replace: true,
      }),
    );
    expect(navigate).toHaveBeenCalledTimes(1);
    expect(queryByTestId("org-outlet")).toBeNull();
  });

  it("lets an admin in without redirecting", () => {
    mockUser.current = { isAdmin: true };
    const { getByTestId } = render(<Layout />);
    expect(getByTestId("org-outlet")).toBeTruthy();
    expect(navigate).not.toHaveBeenCalled();
  });
});
