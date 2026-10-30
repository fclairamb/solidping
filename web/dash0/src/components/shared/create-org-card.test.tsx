/**
 * @vitest-environment jsdom
 *
 * A brand-new account an MCP client sent to /login has no org, so it lands on
 * /no-org. Creating its org there must resume the pending OAuth authorization
 * instead of dropping it on the new org's dashboard.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import "@/i18n";
import { CreateOrgCard } from "./create-org-card";

const navigate = vi.hoisted(() => vi.fn());
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

const mutateAsync = vi.hoisted(() => vi.fn());
vi.mock("@/api/hooks", () => ({
  useCreateOrg: () => ({ mutateAsync, isPending: false }),
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ refreshUser: () => Promise.resolve() }),
}));

vi.mock("@/api/client", () => ({
  ApiError: class extends Error {},
  setSession: vi.fn(),
}));

const assign = vi.fn();

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

function submit(returnTo?: string) {
  vi.stubGlobal("location", { ...window.location, assign });
  mutateAsync.mockResolvedValue({ slug: "acme", accessToken: "t", refreshToken: "r", expiresIn: 60 });
  render(<CreateOrgCard suggestedName="Acme" suggestedSlug="acme" returnTo={returnTo} />);
  fireEvent.click(screen.getByTestId("create-org-submit"));
}

describe("CreateOrgCard", () => {
  it("resumes a pending MCP authorization once the org exists", async () => {
    const authorize = "/api/v1/oauth/authorize?client_id=acme-client&state=s1";
    submit(authorize);
    await waitFor(() => expect(assign).toHaveBeenCalledWith(authorize));
    expect(navigate).not.toHaveBeenCalled();
  });

  it("lands on the new org without a pending flow", async () => {
    submit();
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({ to: "/orgs/$org", params: { org: "acme" } }),
    );
    expect(assign).not.toHaveBeenCalled();
  });
});
