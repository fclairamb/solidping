import { describe, expect, it } from "vitest";

import type { Integration, ReportSchedule, StatusPage } from "@/api/hooks";
import {
  allStepsComplete,
  completedStepCount,
  deriveOnboardingSteps,
  hasEnabledReportSchedule,
  hasNotifiableIntegration,
  pickTestAlertIntegration,
  ONBOARDING_STEP_IDS,
  type OnboardingStepId,
} from "@/lib/onboarding-checklist";

// The checklist stores NO per-step flags — every tick is derived here. These
// tests are the guard on that: if someone later "optimizes" a step into a
// remembered boolean, the derivation stops being the single source of truth
// and a user who joins an already-configured org sees a lie.

function integration(overrides: Partial<Integration> = {}): Integration {
  return {
    uid: overrides.uid ?? "int-1",
    type: overrides.type ?? "email",
    name: overrides.name ?? "Email alerts",
    enabled: overrides.enabled ?? true,
    isDefault: overrides.isDefault ?? false,
    createdAt: "2026-08-28T00:00:00Z",
    updatedAt: "2026-08-28T00:00:00Z",
  };
}

function schedule(enabled: boolean): ReportSchedule {
  return {
    uid: `sched-${enabled}`,
    name: "Weekly uptime",
    frequency: "weekly",
    timezone: "UTC",
    recipients: ["alice@acme.com"],
    checkUids: [],
    checkGroupUids: [],
    includeSlos: false,
    enabled,
    createdAt: "2026-08-28T00:00:00Z",
    updatedAt: "2026-08-28T00:00:00Z",
  } as ReportSchedule;
}

const statusPage = { uid: "sp-1", name: "Status", slug: "status" } as StatusPage;

function stepDone(
  steps: ReturnType<typeof deriveOnboardingSteps>,
  id: OnboardingStepId,
): boolean {
  const step = steps.find((s) => s.id === id);
  expect(step, `step ${id} must exist`).toBeDefined();
  return step!.done;
}

describe("hasNotifiableIntegration", () => {
  it("accepts any notifiable type, so a Slack-only org is not nagged about email", () => {
    expect(
      hasNotifiableIntegration([integration({ type: "slack", uid: "s" })]),
    ).toBe(true);
    expect(hasNotifiableIntegration([integration({ type: "webhook" })])).toBe(
      true,
    );
  });

  it("ignores disabled channels and data-source-only types", () => {
    // Positive control: the same integration enabled does count, so each
    // `false` below is about the attribute under test.
    expect(hasNotifiableIntegration([integration({ type: "slack" })])).toBe(true);

    expect(
      hasNotifiableIntegration([integration({ type: "slack", enabled: false })]),
    ).toBe(false);
    expect(hasNotifiableIntegration([integration({ type: "freebox" })])).toBe(
      false,
    );
    expect(
      hasNotifiableIntegration([integration({ type: "kubernetes" })]),
    ).toBe(false);
  });

  it("treats missing data as not-done rather than crashing", () => {
    expect(hasNotifiableIntegration(undefined)).toBe(false);
    expect(hasNotifiableIntegration([])).toBe(false);
  });
});

describe("hasEnabledReportSchedule", () => {
  it("counts only enabled schedules — a disabled one sends nothing", () => {
    expect(hasEnabledReportSchedule([schedule(true)])).toBe(true);
    expect(hasEnabledReportSchedule([schedule(false)])).toBe(false);
    expect(hasEnabledReportSchedule(undefined)).toBe(false);
  });
});

describe("pickTestAlertIntegration", () => {
  it("prefers the default email channel", () => {
    const picked = pickTestAlertIntegration([
      integration({ uid: "first", type: "email" }),
      integration({ uid: "default", type: "email", isDefault: true }),
    ]);
    expect(picked?.uid).toBe("default");
  });

  it("falls back to the first enabled email channel", () => {
    const picked = pickTestAlertIntegration([
      integration({ uid: "off", type: "email", enabled: false }),
      integration({ uid: "on", type: "email" }),
    ]);
    expect(picked?.uid).toBe("on");
  });

  it("returns nothing when the org has no email channel at all", () => {
    // Positive control: with an email channel present it does pick one, so
    // the undefined below is about the type and not about the fixture.
    expect(pickTestAlertIntegration([integration({ type: "email" })])).toBeDefined();

    expect(
      pickTestAlertIntegration([integration({ type: "slack" })]),
    ).toBeUndefined();
    expect(pickTestAlertIntegration(undefined)).toBeUndefined();
  });
});

describe("deriveOnboardingSteps", () => {
  it("renders the five steps in a stable order", () => {
    const steps = deriveOnboardingSteps({ totalChecks: 0 });
    expect(steps.map((s) => s.id)).toEqual([...ONBOARDING_STEP_IDS]);
  });

  it("ticks nothing for a brand-new, empty org", () => {
    const steps = deriveOnboardingSteps({ totalChecks: 0 });
    expect(completedStepCount(steps)).toBe(0);
    expect(allStepsComplete(steps)).toBe(false);
  });

  it("starts a self-created org at 3/5 — the seeded email channel and weekly report count", () => {
    // Spec 2026-08-28-15 seeds both for every org created through POST /orgs.
    // That head start is deliberate: the checklist reports the truth rather
    // than asking the user to redo what the server already did.
    const steps = deriveOnboardingSteps({
      totalChecks: 1,
      integrations: [integration({ isDefault: true })],
      reportSchedules: [schedule(true)],
      statusPages: [],
      memberCount: 1,
      pendingInvitationCount: 0,
    });

    expect(completedStepCount(steps)).toBe(3);
    expect(stepDone(steps, "check")).toBe(true);
    expect(stepDone(steps, "alerts")).toBe(true);
    expect(stepDone(steps, "report")).toBe(true);
    expect(stepDone(steps, "statusPage")).toBe(false);
    expect(stepDone(steps, "team")).toBe(false);
  });

  it("ticks the team step for a second member or a pending invitation", () => {
    const withMember = deriveOnboardingSteps({
      totalChecks: 1,
      memberCount: 2,
      pendingInvitationCount: 0,
    });
    expect(stepDone(withMember, "team")).toBe(true);

    const withInvite = deriveOnboardingSteps({
      totalChecks: 1,
      memberCount: 1,
      pendingInvitationCount: 1,
    });
    expect(stepDone(withInvite, "team")).toBe(true);
  });

  it("does not tick the team step on an unknown invitation count alone", () => {
    // A non-admin cannot list invitations, so the count is undefined. That
    // must read as "unknown", never as "there is one".
    const steps = deriveOnboardingSteps({
      totalChecks: 1,
      memberCount: 1,
      pendingInvitationCount: undefined,
    });
    expect(stepDone(steps, "team")).toBe(false);
  });

  it("reports all-complete only when every step is genuinely satisfied", () => {
    const almost = deriveOnboardingSteps({
      totalChecks: 1,
      integrations: [integration()],
      reportSchedules: [schedule(true)],
      statusPages: [statusPage],
      memberCount: 1,
    });
    expect(allStepsComplete(almost)).toBe(false);

    const complete = deriveOnboardingSteps({
      totalChecks: 1,
      integrations: [integration()],
      reportSchedules: [schedule(true)],
      statusPages: [statusPage],
      memberCount: 2,
    });
    expect(allStepsComplete(complete)).toBe(true);
    expect(completedStepCount(complete)).toBe(5);
  });
});
