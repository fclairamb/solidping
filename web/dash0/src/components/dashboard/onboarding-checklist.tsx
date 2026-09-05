import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  Bell,
  Check,
  Globe,
  ListChecks,
  Loader2,
  Mail,
  Rocket,
  Send,
  Sparkles,
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
import { Progress } from "@/components/ui/progress";

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
 *
 * Spec 2026-09-03-01 reworked the *look* only: gradient chrome with blurred
 * accent blobs, a real progress bar, a "next up" focal row, a filled emerald
 * tick instead of a strikethrough, a celebratory all-set strip, and a short
 * staggered reveal. Every animation is behind Tailwind's `motion-safe:`
 * variant — this card is the surface that sets that convention for dash0.
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
  // The first still-open step is the card's focal point: it is the one thing
  // the user is being asked to do next, so it gets the ring, the glow and the
  // only `default`-variant CTA. -1 (nothing pending) simply matches no row.
  const nextUpIndex = steps.findIndex((step) => !step.done);
  const progressLabel = t("onboarding.progress", { done, total: steps.length });

  return (
    <Card
      data-testid="onboarding-checklist"
      className="relative overflow-hidden border-primary/30 shadow-primary"
    >
      {/* Decoration only. Every layer here is `pointer-events-none` so it can
          never intercept a click meant for a row's stretched link, and the
          whole thing is `aria-hidden` so it adds nothing to the a11y tree.
          Blobs use alpha tints (primary + chart-5 violet) so they read on
          both the light and the dark card surface without a `dark:` variant;
          no green/amber/red in the chrome, which mean up/degraded/down
          everywhere else on this dashboard. */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 overflow-hidden rounded-xl"
      >
        <span className="absolute inset-x-0 top-0 block h-1 bg-gradient-to-r from-primary via-chart-5 to-primary/30" />
        <span className="absolute -right-16 -top-24 block h-56 w-56 rounded-full bg-primary/10 blur-3xl" />
        <span className="absolute -bottom-24 -left-12 block h-52 w-52 rounded-full bg-chart-5/10 blur-3xl" />
      </div>

      <CardHeader className="relative flex flex-row flex-wrap items-start justify-between gap-3 space-y-0">
        <div className="flex min-w-0 flex-1 items-start gap-3">
          <span
            aria-hidden="true"
            className="hidden h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br from-primary to-chart-5 text-primary-foreground shadow-primary sm:flex"
          >
            <Rocket className="h-4 w-4" />
          </span>
          <div className="min-w-0 flex-1 space-y-1.5">
            <CardTitle className="text-base">{t("onboarding.title")}</CardTitle>
            <CardDescription data-testid="onboarding-progress">
              {progressLabel}
            </CardDescription>
            {/* Progress defaults to `destructiveWhenFull`, which would paint a
                finished checklist RED — the one colour that means "down" on
                this dashboard. Finishing is an achievement: emerald. */}
            <Progress
              value={done}
              max={steps.length}
              destructiveWhenFull={false}
              aria-label={progressLabel}
              className="h-1.5 max-w-xs bg-primary/10"
              indicatorClassName={
                allSet
                  ? "bg-emerald-500"
                  : "bg-gradient-to-r from-primary to-chart-5"
              }
            />
          </div>
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
      <CardContent className="relative space-y-3">
        {allSet ? (
          <div
            className="rounded-lg border border-emerald-500/30 bg-gradient-to-r from-emerald-500/15 via-emerald-500/10 to-primary/10 p-3 motion-safe:animate-in motion-safe:fade-in motion-safe:zoom-in-95 motion-safe:duration-300"
            data-testid="onboarding-all-set"
          >
            <div className="flex items-start gap-3">
              <span
                aria-hidden="true"
                className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-emerald-500/15 text-emerald-600 dark:text-emerald-400"
              >
                <Sparkles className="h-4 w-4" />
              </span>
              <div className="min-w-0">
                <p className="text-sm font-semibold text-emerald-700 dark:text-emerald-300">
                  {t("onboarding.allSet.title")}
                </p>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  {t("onboarding.allSet.body")}
                </p>
              </div>
            </div>
          </div>
        ) : null}

        <ul className="space-y-2">
          {steps.map((step, index) => (
            <li key={step.id}>
              <OnboardingStepRow
                org={org}
                step={step}
                index={index}
                isNext={index === nextUpIndex}
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
  index,
  isNext,
  onTestAlert,
  testAlertPending,
}: {
  org: string;
  step: OnboardingStep;
  /** 0-based position in the list — drives the badge number and the stagger. */
  index: number;
  /** True for the first still-open step: the card's single focal point. */
  isNext: boolean;
  onTestAlert?: () => void;
  testAlertPending: boolean;
}) {
  const { t } = useTranslation("dashboard");
  const Icon = STEP_ICONS[step.id];

  // Three tones, not two: done rows recede into a faint emerald wash, the
  // next-up row is ringed and lifted so the eye lands on it first, and the
  // remaining pending rows sit quietly in between. All three use alpha tints
  // over the card surface so neither theme needs a `dark:` variant.
  const rowTone = step.done
    ? "border-emerald-500/25 bg-emerald-500/[0.07] hover:border-emerald-500/40 hover:shadow-card-hover"
    : isNext
      ? "border-primary/45 bg-card shadow-primary ring-1 ring-primary/15 hover:border-primary/60 hover:shadow-primary-hover"
      : "border-border/70 bg-card/60 hover:border-primary/30 hover:bg-card hover:shadow-card-hover";

  return (
    <div
      className={`relative flex cursor-pointer flex-col gap-2 rounded-lg border p-3 transition-all duration-200 motion-safe:animate-in motion-safe:fade-in motion-safe:slide-in-from-bottom-2 motion-safe:fill-mode-both motion-safe:hover:-translate-y-px sm:flex-row sm:items-center sm:gap-3 ${rowTone}`}
      // Short, one-shot stagger on mount (≤240ms of delay, 200ms each) so the
      // list assembles instead of appearing. The delay is inert without an
      // animation, which is exactly what `prefers-reduced-motion` produces:
      // the `motion-safe:` classes above simply do not apply.
      style={{ animationDelay: `${index * 60}ms` }}
      data-testid={`onboarding-step-${step.id}`}
      data-done={step.done ? "true" : "false"}
      // Exposed so the e2e suite can pin the focal row rather than guessing
      // it from a class name: exactly one row carries data-next="true", and
      // it is always the first step still open.
      data-next={isNext ? "true" : "false"}
    >
      <span
        className="shrink-0"
        data-testid={`onboarding-step-${step.id}-status`}
        data-done={step.done ? "true" : "false"}
        aria-label={step.done ? t("onboarding.doneLabel") : t("onboarding.todoLabel")}
      >
        {/* Keyed on the state so React remounts the well when a step flips —
            that is what replays the tick's zoom-in, making the transition
            something the user sees rather than something that just happened. */}
        {step.done ? (
          <span
            key="done"
            className="flex h-6 w-6 items-center justify-center rounded-full bg-emerald-500 text-white shadow-sm motion-safe:animate-in motion-safe:zoom-in-50 motion-safe:duration-300"
          >
            <Check className="h-3.5 w-3.5" strokeWidth={3} aria-hidden="true" />
          </span>
        ) : (
          <span
            key="todo"
            className={`flex h-6 w-6 items-center justify-center rounded-full border text-[11px] font-semibold tabular-nums transition-colors ${
              isNext
                ? "border-primary/50 bg-primary/10 text-primary"
                : "border-border bg-muted/60 text-muted-foreground"
            }`}
          >
            {index + 1}
          </span>
        )}
      </span>

      <div className="min-w-0 flex-1">
        <p
          className={`flex flex-wrap items-center gap-1.5 text-sm font-medium ${
            step.done ? "text-muted-foreground" : ""
          }`}
        >
          <Icon
            className={`h-3.5 w-3.5 shrink-0 ${
              step.done
                ? "text-emerald-600/70 dark:text-emerald-400/70"
                : isNext
                  ? "text-primary"
                  : "text-muted-foreground"
            }`}
            aria-hidden="true"
          />
          <span className="min-w-0 break-words">
            {t(`onboarding.steps.${step.id}.title`)}
          </span>
          {isNext ? (
            <span className="shrink-0 rounded-full bg-primary px-1.5 py-px text-[10px] font-semibold uppercase leading-4 tracking-wide text-primary-foreground">
              {t("onboarding.nextUp")}
            </span>
          ) : null}
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
          isNext={isNext}
          sharesRowWithTestAlert={step.id === "alerts" && Boolean(onTestAlert)}
        />
      </div>
    </div>
  );
}

function OnboardingStepLink({
  org,
  step,
  isNext,
  sharesRowWithTestAlert,
}: {
  org: string;
  step: OnboardingStep;
  /** True for the first still-open step. */
  isNext: boolean;
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
  // Exactly one filled step CTA per card: the next-up step's own. A done
  // step, a pending step further down the list, and the alerts row that
  // already has the test-alert button leading it all fall back to `outline`,
  // so the eye lands on one obvious next action. (The test-alert button is
  // its own affordance, not a step CTA, and keeps its own weight.)
  const variant =
    isNext && !step.done && !sharesRowWithTestAlert ? "default" : "outline";

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
        // CTA — without nesting a second interactive element. The card's
        // decorative blob layer is `pointer-events-none` precisely so it
        // cannot steal this hit target.
        className="after:absolute after:inset-0"
        data-testid={testId}
      >
        {label}
      </Link>
    </Button>
  );
}
