import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  Bell,
  CheckCircle2,
  Circle,
  Globe,
  ListChecks,
  Loader2,
  Mail,
  Send,
  Users,
  X,
} from "lucide-react";
import {
  onboardingUiStateKey,
  useIntegrations,
  useInvitations,
  useMembers,
  useReportSchedules,
  useSetUiState,
  useStatusPages,
  useTestIntegration,
  useUiState,
  type OnboardingUiState,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { useAuth } from "@/contexts/AuthContext";
import {
  allStepsComplete,
  completedStepCount,
  deriveOnboardingSteps,
  pickTestAlertIntegration,
  type OnboardingStep,
  type OnboardingStepId,
} from "@/lib/onboarding-checklist";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

// The checklist derives everything from list endpoints that barely change on
// an onboarding timescale, so it holds its answers for a while instead of
// joining the dashboard's 30s poll. Creating an integration or a status page
// invalidates the matching query key anyway, so the card still flips
// immediately when the user does the thing it is asking for.
const ONBOARDING_STALE_MS = 60_000;

const STEP_ICONS: Record<OnboardingStepId, typeof ListChecks> = {
  check: ListChecks,
  alerts: Bell,
  report: Mail,
  statusPage: Globe,
  team: Users,
};

interface OnboardingChecklistProps {
  org: string;
  /** Org-wide check count from the stats endpoint. */
  totalChecks: number;
}

/**
 * Getting-started checklist (spec 2026-08-28-17).
 *
 * Replaces the old one-shot `FirstResultCelebration` banner, whose dismissal
 * lived in localStorage and therefore came back on every other device while
 * pointing at nothing but the check page. This card:
 *
 *   - derives every step from real resources (no stored per-step flags), so
 *     it is correct for a user who joins an already-configured org, and
 *   - stores its dismissal server-side per user per org, so hiding it here
 *     hides it everywhere and the account page can bring it back.
 *
 * It only renders once the org has at least one check — the empty-state hero
 * owns the moment before that.
 */
export function OnboardingChecklist({
  org,
  totalChecks,
}: OnboardingChecklistProps) {
  const { t } = useTranslation("dashboard");
  const { user } = useAuth();
  // Listing invitations is admin-only, so only an admin-capable member can
  // contribute that half of the team step.
  const isAdmin = Boolean(user?.isAdmin);

  const stateKey = onboardingUiStateKey(org);
  const dismissalQuery = useUiState<OnboardingUiState>(stateKey, {
    staleTime: ONBOARDING_STALE_MS,
  });
  const setDismissal = useSetUiState<OnboardingUiState>(stateKey);

  const dismissalLoaded = !dismissalQuery.isPending;
  const dismissed = Boolean(dismissalQuery.data?.dismissedAt);

  // Once dismissed, the card costs zero requests: every derivation query is
  // switched off rather than merely ignored.
  const derivationEnabled = dismissalLoaded && !dismissed && totalChecks >= 1;
  const listOpts = {
    enabled: derivationEnabled,
    staleTime: ONBOARDING_STALE_MS,
  };

  const integrationsQuery = useIntegrations(org, listOpts);
  const reportSchedulesQuery = useReportSchedules(org, listOpts);
  const statusPagesQuery = useStatusPages(org, { enabled: derivationEnabled });
  const membersQuery = useMembers(org, listOpts);
  // Listing invitations is admin-only; asking as a plain member would 403 on
  // every dashboard load. Their member count alone decides the team step.
  const invitationsQuery = useInvitations(org, {
    ...listOpts,
    enabled: derivationEnabled && isAdmin,
  });

  const steps = useMemo(
    () =>
      deriveOnboardingSteps({
        totalChecks,
        integrations: integrationsQuery.data,
        reportSchedules: reportSchedulesQuery.data,
        statusPages: statusPagesQuery.data,
        memberCount: membersQuery.data?.data?.length,
        pendingInvitationCount: invitationsQuery.data?.data?.length,
      }),
    [
      totalChecks,
      integrationsQuery.data,
      reportSchedulesQuery.data,
      statusPagesQuery.data,
      membersQuery.data,
      invitationsQuery.data,
    ],
  );

  const allSet = allStepsComplete(steps);

  // A fully configured org must not carry this card forever: the moment every
  // step is done we write the same dismissal the "X" writes. The write is
  // fire-and-forget and guarded by a ref rather than state, so it never
  // cascades a render; the card stays on screen for the rest of the visit
  // (react-query keeps the derivation data cached even once the queries are
  // switched off) so the user actually sees the finish line instead of a card
  // that vanishes mid-blink. A reload then finds the dismissal and skips it.
  const selfDismissWritten = useRef(false);
  const shouldSelfDismiss = derivationEnabled && allSet;

  useEffect(() => {
    if (!shouldSelfDismiss || selfDismissWritten.current) return;
    selfDismissWritten.current = true;
    setDismissal.mutate({ dismissedAt: new Date().toISOString() });
    // setDismissal is a stable react-query mutation object; listing it would
    // re-run this effect on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shouldSelfDismiss]);

  // Hiding by hand must take effect immediately, without waiting for the PUT
  // to land — and without being undone by the `allSet` escape hatch above.
  const [manuallyDismissed, setManuallyDismissed] = useState(false);

  const testAlertIntegration = pickTestAlertIntegration(integrationsQuery.data);
  const testIntegration = useTestIntegration(org);

  const handleTestAlert = () => {
    if (!testAlertIntegration) return;

    testIntegration.mutate(testAlertIntegration.uid, {
      onSuccess: (result) => {
        // The endpoint answers 200 whether or not delivery worked, so the
        // `success` field is the only truth. A failure (SMTP unconfigured on
        // a self-hosted install, most often) must show what the server said,
        // not a generic shrug and not a fake success.
        if (result.success) {
          toast.success(t("onboarding.testAlert.sent"), {
            description: result.detail || t("onboarding.testAlert.sentDetail"),
          });
        } else {
          toast.error(t("onboarding.testAlert.failed"), {
            description: result.error || t("onboarding.testAlert.failedFallback"),
          });
        }
      },
      onError: (err) => {
        toast.error(t("onboarding.testAlert.failed"), {
          description:
            err instanceof ApiError
              ? err.message
              : t("onboarding.testAlert.failedFallback"),
        });
      },
    });
  };

  const handleDismiss = () => {
    setManuallyDismissed(true);
    setDismissal.mutate({ dismissedAt: new Date().toISOString() });
  };

  // Never before the first check (the empty-state hero owns that moment), and
  // never while we still do not know whether the user hid it — rendering
  // first and retracting would flash the card on every page load.
  if (totalChecks < 1 || !dismissalLoaded || manuallyDismissed) return null;
  if (dismissed && !allSet) return null;

  return (
    <OnboardingChecklistCard
      org={org}
      steps={steps}
      allSet={allSet}
      onDismiss={handleDismiss}
      onTestAlert={testAlertIntegration ? handleTestAlert : undefined}
      testAlertPending={testIntegration.isPending}
    />
  );
}

export interface OnboardingChecklistCardProps {
  org: string;
  steps: OnboardingStep[];
  allSet: boolean;
  onDismiss?: () => void;
  /** Omitted when the org has no email channel to send a test alert through. */
  onTestAlert?: () => void;
  testAlertPending?: boolean;
}

/**
 * The card's presentation, free of data fetching so the design reference can
 * render it with fixture props.
 */
export function OnboardingChecklistCard({
  org,
  steps,
  allSet,
  onDismiss,
  onTestAlert,
  testAlertPending = false,
}: OnboardingChecklistCardProps) {
  const { t } = useTranslation("dashboard");
  const done = completedStepCount(steps);

  return (
    <Card
      data-testid="onboarding-checklist"
      className="border-primary/30 bg-primary/5"
    >
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2 space-y-0">
        <div className="min-w-0">
          <CardTitle className="text-base">{t("onboarding.title")}</CardTitle>
          <CardDescription data-testid="onboarding-progress">
            {t("onboarding.progress", { done, total: steps.length })}
          </CardDescription>
        </div>
        {onDismiss ? (
          <Button
            variant="ghost"
            size="icon"
            className="-mr-2 -mt-1 shrink-0"
            onClick={onDismiss}
            aria-label={t("onboarding.dismiss")}
            data-testid="onboarding-dismiss"
          >
            <X className="h-4 w-4" />
          </Button>
        ) : null}
      </CardHeader>
      <CardContent className="space-y-3">
        {allSet ? (
          <div
            className="rounded-md border border-emerald-500/30 bg-emerald-500/10 p-3"
            data-testid="onboarding-all-set"
          >
            <p className="text-sm font-medium text-emerald-700 dark:text-emerald-300">
              {t("onboarding.allSet.title")}
            </p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t("onboarding.allSet.body")}
            </p>
          </div>
        ) : null}

        <ul className="space-y-2">
          {steps.map((step) => (
            <li key={step.id}>
              <OnboardingStepRow
                org={org}
                step={step}
                onTestAlert={onTestAlert}
                testAlertPending={testAlertPending}
              />
            </li>
          ))}
        </ul>

        <p className="text-xs text-muted-foreground">
          {t("onboarding.reenableHint")}
        </p>
      </CardContent>
    </Card>
  );
}

function OnboardingStepRow({
  org,
  step,
  onTestAlert,
  testAlertPending,
}: {
  org: string;
  step: OnboardingStep;
  onTestAlert?: () => void;
  testAlertPending: boolean;
}) {
  const { t } = useTranslation("dashboard");
  const Icon = STEP_ICONS[step.id];

  // Rows had no background of their own, so the card's own primary tint
  // bled through every one of them — done and pending rows read identically
  // apart from the tick. Give each row an explicit base (matching the card
  // surface) so it reads as a crisp item sitting on the tint, and let done
  // rows carry a faint emerald wash — the same alpha-based
  // `emerald-500/…` pattern as the "all set" banner above, which needs no
  // `dark:` variant because the alpha already reads correctly on both
  // surfaces.
  const rowTone = step.done
    ? "border-emerald-500/20 bg-emerald-500/10 hover:bg-emerald-500/15"
    : "bg-card hover:bg-muted/50";

  return (
    <div
      className={`relative flex cursor-pointer flex-col gap-2 rounded-md border p-3 transition-colors sm:flex-row sm:items-center sm:gap-3 ${rowTone}`}
      data-testid={`onboarding-step-${step.id}`}
      data-done={step.done ? "true" : "false"}
    >
      <span
        className="shrink-0"
        data-testid={`onboarding-step-${step.id}-status`}
        data-done={step.done ? "true" : "false"}
        aria-label={step.done ? t("onboarding.doneLabel") : t("onboarding.todoLabel")}
      >
        {step.done ? (
          <CheckCircle2 className="h-5 w-5 text-emerald-600 dark:text-emerald-400" />
        ) : (
          <Circle className="h-5 w-5 text-muted-foreground" />
        )}
      </span>

      <div className="min-w-0 flex-1">
        <p
          className={`flex items-center gap-1.5 text-sm font-medium ${
            step.done ? "text-muted-foreground line-through" : ""
          }`}
        >
          <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span className="min-w-0 break-words">
            {t(`onboarding.steps.${step.id}.title`)}
          </span>
        </p>
        {step.done ? null : (
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t(`onboarding.steps.${step.id}.description`)}
          </p>
        )}
      </div>

      <div className="flex shrink-0 flex-wrap gap-2">
        {step.id === "alerts" && onTestAlert ? (
          <Button
            size="sm"
            className="relative z-10"
            onClick={onTestAlert}
            disabled={testAlertPending}
            data-testid="onboarding-test-alert"
          >
            {testAlertPending ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Send className="mr-1.5 h-3.5 w-3.5" />
            )}
            {t("onboarding.testAlert.cta")}
          </Button>
        ) : null}
        <OnboardingStepLink
          org={org}
          step={step}
          sharesRowWithTestAlert={step.id === "alerts" && Boolean(onTestAlert)}
        />
      </div>
    </div>
  );
}

function OnboardingStepLink({
  org,
  step,
  sharesRowWithTestAlert,
}: {
  org: string;
  step: OnboardingStep;
  /** True only for the alerts row when the test-alert button is also shown. */
  sharesRowWithTestAlert: boolean;
}) {
  const { t } = useTranslation("dashboard");
  // The statusPage step is the one CTA whose pending label ("Create a status
  // page") stops making sense once done — reusing it there reads as an
  // instruction to redo a finished step. Every other step already has a
  // state-neutral label (e.g. "check" says "View checks" throughout).
  const label =
    step.done && step.id === "statusPage"
      ? t("onboarding.steps.statusPage.ctaDone")
      : t(`onboarding.steps.${step.id}.cta`);
  const testId = `onboarding-step-${step.id}-cta`;
  // The step's own action is secondary once another button leads the row
  // (the test-alert case) and once the step is done. When the alerts row has
  // no test-alert button to defer to, its own CTA is the row's only action
  // and should carry the same weight as any other pending step's.
  const variant =
    step.done || sharesRowWithTestAlert ? "outline" : "default";

  // The statusPage step used to land on the blank create form (pre-attaching
  // the org's first check via ?checkUid=, spec 2026-08-28-16). It now lands
  // on the list instead (spec 2026-08-30-10): once the list carries its own
  // "create a page for me" wand, dropping the user into a blank form skips
  // the better entry point — the list is where they choose between the wand
  // and the manual form. The ?checkUid= prefill on the new-page route itself
  // stays; other callers (e.g. a check's "Publish on a status page" link)
  // still use it.
  const to = {
    check: "/orgs/$org/checks",
    alerts: "/orgs/$org/integrations",
    report: "/orgs/$org/organization/report-schedules",
    statusPage: "/orgs/$org/status-pages",
    team: "/orgs/$org/organization/members",
  }[step.id];

  return (
    <Button asChild size="sm" variant={variant}>
      <Link
        to={to}
        params={{ org }}
        // Stretches over the whole row via the `after` pseudo-element
        // (positioned against the row's `relative` ancestor), so the entire
        // step row is one click/keyboard target instead of only this small
        // CTA — without nesting a second interactive element.
        className="after:absolute after:inset-0"
        data-testid={testId}
      >
        {label}
      </Link>
    </Button>
  );
}
