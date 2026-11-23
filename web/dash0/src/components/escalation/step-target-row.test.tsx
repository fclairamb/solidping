/**
 * @vitest-environment jsdom
 *
 * A target picker starts with no target (targetUid "") and gets one once the
 * user picks it. The Radix Select must stay controlled for its whole lifetime:
 * passing `undefined` for the empty value made React warn "Select is changing
 * from uncontrolled to controlled".
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";

vi.mock("@/api/hooks", () => ({
  useOnCallSchedules: () => ({
    data: [{ uid: "sched-1", name: "Acme primary" }],
    isLoading: false,
  }),
  useMembers: () => ({
    data: { data: [{ userUid: "user-1", name: "Ada", email: "ada@acme.com" }] },
    isLoading: false,
  }),
  useIntegrations: () => ({
    data: [{ uid: "conn-1", name: "Acme Slack", type: "slack", enabled: true }],
    isLoading: false,
  }),
}));

vi.mock("@/components/notifications/member-coverage", () => ({
  EmailOnlyBadge: () => null,
  useEmailOnlyUserUids: () => new Set<string>(),
}));

const { StepTargetRow } = await import("./step-target-row");

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const cases = [
  { type: "schedule", uid: "sched-1", label: "Acme primary" },
  { type: "user", uid: "user-1", label: "Ada" },
  { type: "connection", uid: "conn-1", label: "Acme Slack" },
] as const;

describe("StepTargetRow target select", () => {
  for (const { type, uid, label } of cases) {
    it(`stays controlled when a ${type} target is picked`, () => {
      const errors = vi.spyOn(console, "error").mockImplementation(() => {});
      const warns = vi.spyOn(console, "warn").mockImplementation(() => {});

      const { rerender } = render(
        <StepTargetRow org="acme" target={{ type, targetUid: "" }} onChange={() => {}} />,
      );
      rerender(
        <StepTargetRow org="acme" target={{ type, targetUid: uid }} onChange={() => {}} />,
      );

      expect(screen.getAllByText(label, { exact: false }).length).toBeGreaterThan(0);
      const logged = [...errors.mock.calls, ...warns.mock.calls].flat().map(String).join("\n");
      expect(logged).not.toMatch(/uncontrolled to controlled/);
    });
  }
});
