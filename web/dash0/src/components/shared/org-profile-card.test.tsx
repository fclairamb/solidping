/**
 * @vitest-environment jsdom
 *
 * "Use an external URL instead" is the user's choice: a background refresh
 * of the organization identity that lands after it (the logo upload's own
 * /auth/me) must not flip the card back to the "Uploaded file" badge.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import "@/i18n";

const identity = vi.hoisted(() => ({
  current: { name: "Acme", logoUrl: "/pub/assets/first" },
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ adoptRenamedOrgSession: vi.fn(), refreshUser: vi.fn() }),
  useOrganizationIdentity: () => identity.current,
}));
vi.mock("@/api/hooks", () => {
  const mutation = () => ({ isPending: false, mutateAsync: vi.fn() });
  return {
    useClearOrgLogo: mutation,
    useUpdateOrgProfile: mutation,
    useUploadOrgLogo: mutation,
  };
});

const { OrgProfileCard } = await import("./org-profile-card");

afterEach(() => {
  cleanup();
  identity.current = { name: "Acme", logoUrl: "/pub/assets/first" };
});

describe("OrgProfileCard logo source", () => {
  it("keeps the URL field after the user picked it, when the identity refreshes late", () => {
    const { rerender } = render(<OrgProfileCard org="acme" />);
    expect(screen.getByTestId("org-profile-logo-badge")).toBeTruthy();

    fireEvent.click(screen.getByTestId("org-profile-logo-use-url"));
    expect(screen.queryByTestId("org-profile-logo-badge")).toBeNull();

    // The late refresh: same org, a new uploaded logo path.
    identity.current = { name: "Acme", logoUrl: "/pub/assets/second" };
    rerender(<OrgProfileCard org="acme" />);

    expect(screen.getByTestId("org-profile-logo-url")).toBeTruthy();
    expect(screen.queryByTestId("org-profile-logo-badge")).toBeNull();
  });

  it("still follows a refresh when the user has not picked the URL field", () => {
    identity.current = { name: "Acme", logoUrl: "" };
    const { rerender } = render(<OrgProfileCard org="acme" />);
    expect(screen.getByTestId("org-profile-logo-url")).toBeTruthy();

    identity.current = { name: "Acme", logoUrl: "/pub/assets/first" };
    rerender(<OrgProfileCard org="acme" />);

    expect(screen.getByTestId("org-profile-logo-badge")).toBeTruthy();
  });
});
