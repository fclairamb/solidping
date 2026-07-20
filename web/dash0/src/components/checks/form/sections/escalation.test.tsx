/**
 * @vitest-environment jsdom
 *
 * Component tests for EscalationSelect's variant="group" generalization
 * (spec 2026-07-20-01): a check-group's own picker has one fewer level in
 * the inherit chain than a check's (no group to resolve through — inherit
 * goes straight to the org default), while variant="check" (the default)
 * keeps its existing group -> org-default resolution unchanged.
 *
 * The org-settings query is admin-only; a non-admin's request errors/returns
 * undefined, and the picker must fall back to an "unknown default" label
 * rather than break.
 */
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { EscalationSelect } from "./escalation";
import type { CheckGroup, EscalationPolicy } from "@/api/hooks";

const mocks = vi.hoisted(() => ({
  useEscalationPolicies: vi.fn(),
  useOrgSettings: vi.fn(),
  useCreateEscalationPolicy: vi.fn(),
}));

vi.mock("@/api/hooks", () => ({
  useEscalationPolicies: mocks.useEscalationPolicies,
  useOrgSettings: mocks.useOrgSettings,
  useCreateEscalationPolicy: mocks.useCreateEscalationPolicy,
}));

// EscalationSelect's footer note links to "Manage policies" via
// @tanstack/react-router's <Link>, which needs a <RouterProvider> in scope.
// That link isn't under test here — stub it to a plain anchor so the
// component can render standalone.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: PropsWithChildren<Record<string, unknown>>) => (
    <a {...props}>{children}</a>
  ),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const ORG_POLICY: EscalationPolicy = {
  uid: "policy-org-default",
  name: "Org Default Policy",
  repeatMax: 0,
  stepCount: 1,
};

const GROUP_POLICY: EscalationPolicy = {
  uid: "policy-group",
  name: "Group Policy",
  repeatMax: 0,
  stepCount: 1,
};

const GROUP: CheckGroup = {
  uid: "group-1",
  name: "Some Group",
  slug: "some-group",
  sortOrder: 0,
  checkCount: 0,
  escalationPolicyUid: GROUP_POLICY.uid,
  createdAt: "",
  updatedAt: "",
};

function setup({
  policies = [ORG_POLICY, GROUP_POLICY],
  defaultEscalationPolicyUid,
  orgSettingsAvailable = true,
}: {
  policies?: EscalationPolicy[];
  defaultEscalationPolicyUid?: string | null;
  orgSettingsAvailable?: boolean;
} = {}) {
  mocks.useEscalationPolicies.mockReturnValue({ data: policies });
  mocks.useOrgSettings.mockReturnValue({
    data: orgSettingsAvailable
      ? { defaultEscalationPolicyUid }
      : undefined,
  });
  mocks.useCreateEscalationPolicy.mockReturnValue({ mutateAsync: vi.fn() });
}

describe("EscalationSelect variant='group'", () => {
  it("resolves inherit straight to the org default (no group level)", () => {
    setup({ defaultEscalationPolicyUid: ORG_POLICY.uid });

    render(
      <EscalationSelect
        org="test"
        variant="group"
        value=""
        onChange={() => {}}
      />,
    );

    const trigger = screen.getByTestId("escalation-policy-select");
    expect(trigger.textContent).toContain("Inherit");
    expect(trigger.textContent).toContain(ORG_POLICY.name);
  });

  it('shows "nothing" when no org default is set', () => {
    setup({ defaultEscalationPolicyUid: null });

    render(
      <EscalationSelect
        org="test"
        variant="group"
        value=""
        onChange={() => {}}
      />,
    );

    const trigger = screen.getByTestId("escalation-policy-select");
    expect(trigger.textContent).toContain("Inherit");
    expect(trigger.textContent).toContain("nothing");
  });

  it("ignores checkGroupUid/checkGroups even if passed by mistake", () => {
    setup({ defaultEscalationPolicyUid: ORG_POLICY.uid });

    render(
      <EscalationSelect
        org="test"
        variant="group"
        value=""
        onChange={() => {}}
        checkGroupUid={GROUP.uid}
        checkGroups={[GROUP]}
      />,
    );

    // Must resolve to the org default, not the "group's own" policy — a
    // group being edited has no further group to inherit from.
    const trigger = screen.getByTestId("escalation-policy-select");
    expect(trigger.textContent).toContain(ORG_POLICY.name);
    expect(trigger.textContent).not.toContain(GROUP_POLICY.name);
  });

  it("falls back to an unknown default for a non-admin (org settings query errors)", () => {
    setup({ orgSettingsAvailable: false });

    render(
      <EscalationSelect
        org="test"
        variant="group"
        value=""
        onChange={() => {}}
      />,
    );

    const trigger = screen.getByTestId("escalation-policy-select");
    expect(trigger.textContent).toContain("Inherit");
    expect(trigger.textContent).toContain("nothing");
  });
});

describe("EscalationSelect variant='check' (regression, default variant)", () => {
  it("still resolves inherit through the check's group, then the org default", () => {
    setup({ defaultEscalationPolicyUid: ORG_POLICY.uid });

    render(
      <EscalationSelect
        org="test"
        value=""
        onChange={() => {}}
        checkGroupUid={GROUP.uid}
        checkGroups={[GROUP]}
      />,
    );

    const trigger = screen.getByTestId("escalation-policy-select");
    // The group's own policy wins over the org default for a check.
    expect(trigger.textContent).toContain(GROUP_POLICY.name);
  });

  it("falls back to the org default when the check's group has no policy", () => {
    setup({ defaultEscalationPolicyUid: ORG_POLICY.uid });
    const groupWithoutPolicy: CheckGroup = { ...GROUP, escalationPolicyUid: null };

    render(
      <EscalationSelect
        org="test"
        value=""
        onChange={() => {}}
        checkGroupUid={groupWithoutPolicy.uid}
        checkGroups={[groupWithoutPolicy]}
      />,
    );

    const trigger = screen.getByTestId("escalation-policy-select");
    expect(trigger.textContent).toContain(ORG_POLICY.name);
  });
});
