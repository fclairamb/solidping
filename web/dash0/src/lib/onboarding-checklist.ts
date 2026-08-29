import {
  canNotify,
  type Integration,
  type ReportSchedule,
  type StatusPage,
} from "@/api/hooks";

/**
 * Getting-started checklist derivation (spec 2026-08-28-17).
 *
 * Every step is derived from resources that already exist — there is no
 * per-step "done" flag anywhere, on the server or in the browser. That is the
 * whole point: a user who wires up alerting through the API, or who joins an
 * org that is already configured, must see the truth rather than a stale
 * tick-list. The only thing that IS stored is the dismissal, and that lives
 * server-side per user per org (`onboarding.<org>` ui-state).
 */

export type OnboardingStepId =
  | "check"
  | "alerts"
  | "report"
  | "statusPage"
  | "team";

/** The order the steps are rendered in — first check first, then the things
 *  it unlocks. */
export const ONBOARDING_STEP_IDS: readonly OnboardingStepId[] = [
  "check",
  "alerts",
  "report",
  "statusPage",
  "team",
] as const;

export interface OnboardingDerivationInput {
  /** Org-wide check count, from the stats endpoint (never `checks.length`). */
  totalChecks: number;
  integrations?: Integration[];
  reportSchedules?: ReportSchedule[];
  statusPages?: StatusPage[];
  /** Members in the org, including the current user. */
  memberCount?: number;
  /** Pending invitations. Undefined for a non-admin, who cannot read them. */
  pendingInvitationCount?: number;
}

export interface OnboardingStep {
  id: OnboardingStepId;
  done: boolean;
}

/**
 * "Get alerted" is satisfied by ANY enabled notifiable integration — a
 * Slack-only org must not be nagged about email. Data-source-only types
 * (Freebox, Kubernetes) do not count: they deliver nothing.
 */
export function hasNotifiableIntegration(integrations?: Integration[]): boolean {
  return (integrations ?? []).some(
    (integration) => integration.enabled && canNotify(integration.type),
  );
}

/**
 * Picks the integration the "Send me a test alert" button fires at: the
 * default email integration if there is one, else the first enabled email
 * integration. Returns undefined when the org has no email channel at all —
 * the button is then hidden rather than firing a "test alert" at, say, a
 * PagerDuty rotation.
 */
export function pickTestAlertIntegration(
  integrations?: Integration[],
): Integration | undefined {
  const emails = (integrations ?? []).filter(
    (integration) => integration.enabled && integration.type === "email",
  );

  return emails.find((integration) => integration.isDefault) ?? emails[0];
}

/** A report schedule counts only while it is enabled — a disabled one sends
 *  nothing, so treating it as done would be a lie. */
export function hasEnabledReportSchedule(
  reportSchedules?: ReportSchedule[],
): boolean {
  return (reportSchedules ?? []).some((schedule) => schedule.enabled);
}

export function deriveOnboardingSteps(
  input: OnboardingDerivationInput,
): OnboardingStep[] {
  return [
    { id: "check", done: input.totalChecks >= 1 },
    { id: "alerts", done: hasNotifiableIntegration(input.integrations) },
    { id: "report", done: hasEnabledReportSchedule(input.reportSchedules) },
    { id: "statusPage", done: (input.statusPages ?? []).length >= 1 },
    {
      id: "team",
      // More than one member, or an invitation still waiting to be accepted.
      // A non-admin cannot read invitations, so `undefined` there means
      // "unknown", never "none" — the member count alone decides for them.
      done:
        (input.memberCount ?? 0) > 1 || (input.pendingInvitationCount ?? 0) > 0,
    },
  ];
}

export function completedStepCount(steps: OnboardingStep[]): number {
  return steps.filter((step) => step.done).length;
}

export function allStepsComplete(steps: OnboardingStep[]): boolean {
  return steps.length > 0 && steps.every((step) => step.done);
}
