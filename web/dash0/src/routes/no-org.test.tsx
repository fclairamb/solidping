/**
 * @vitest-environment jsdom
 *
 * The "join an existing org" card on /no-org is only offered when there is a
 * join reason (?join=<slug>, a pending membership, or existing requests).
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { Route } from "./no-org";

const search = vi.hoisted(() => ({ value: {} as Record<string, unknown> }));

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: Record<string, unknown>) => ({
    options,
    useSearch: () => search.value,
  }),
  useNavigate: () => vi.fn(),
}));

vi.mock("@/api/hooks", () => ({
  useCreateMembershipRequest: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCancelMembershipRequest: () => ({ mutate: vi.fn(), isPending: false }),
  useMyMembershipRequests: () => ({ data: { data: [] }, isLoading: false }),
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ logout: vi.fn(), user: undefined, isLoading: false }),
}));

vi.mock("@/components/shared/create-org-card", () => ({
  CreateOrgCard: () => <div data-testid="create-org-card" />,
}));

function renderPage() {
  const Page = (Route as unknown as { options: { component: () => JSX.Element } })
    .options.component;
  render(<Page />);
}

afterEach(() => {
  cleanup();
  search.value = {};
});

describe("/no-org join card", () => {
  it("is hidden without a join reason", () => {
    renderPage();
    expect(screen.getByTestId("create-org-card")).toBeTruthy();
    expect(screen.queryByTestId("no-org-join-toggle")).toBeNull();
  });

  it("is shown with ?join=<slug>", () => {
    search.value = { join: "acme" };
    renderPage();
    expect(screen.getByTestId("no-org-join-toggle")).toBeTruthy();
  });
});
