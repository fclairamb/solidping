// EvaluationCard explains a passive check's SCHEDULER row (spec
// 2026-09-02-04).
//
// A heartbeat or email check writes two kinds of raw row that used to be
// indistinguishable in the UI:
//
//   - SIGNAL rows (beats) — written at ingest by the heartbeat endpoint or the
//     email receiver. They carry caller metadata (userAgent / remoteAddr /
//     httpMethod / data) and have no worker, hence no region.
//   - EVALUATION rows — written once per period by a checks worker looking at
//     the schedule. They carry a region and, since spec 2026-09-02-04,
//     `evaluation: true` plus the beat they were computed from.
//
// Both used to read "Heartbeat received" with status Up, so a user who opened
// the evaluation row that landed seconds after their ping saw no Caller card
// and concluded the ping had never been recorded. This card is the fix on the
// reading side: it names the row for what it is, says which beat it looked at,
// and links straight to it.
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { CalendarClock, ArrowRight } from "lucide-react";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { formatDuration } from "@/components/shared/relative-time";

type Output = Record<string, unknown>;

// EVALUATION_OUTPUT_KEYS are the keys this card renders itself. The
// result-detail page strips these — and `message`, which is rendered as the
// card's first line — from its raw JSON dump so nothing is shown twice, and so
// the Output card disappears entirely when nothing else remains. Mirrors
// DNSBL_OUTPUT_KEYS / EMAIL_DELIVERY_OUTPUT_KEYS.
export const EVALUATION_OUTPUT_KEYS = [
  "evaluation",
  "lastSignalAt",
  "lastSignalResultUid",
  "overdueBy",
  "runStarted",
] as const;

// CALLER_OUTPUT_KEYS are the keys only an INGEST row ever carries. They are
// the negative half of the legacy heuristic below — a row that has any of them
// came from a real caller and is never an evaluation.
const CALLER_OUTPUT_KEYS = ["userAgent", "remoteAddr", "httpMethod", "data"] as const;

/**
 * isEvaluationOutput reports whether a result row was written by the scheduler
 * rather than by an inbound signal.
 *
 * The contract is the explicit `evaluation: true` flag the worker stamps. The
 * second clause is a courtesy for rows written BEFORE that flag shipped:
 * lastSignalAt / runStarted are keys only the evaluator has ever written, and
 * requiring the absence of every caller key keeps a real beat from ever
 * matching. Those rows age out with raw retention (24 h by default), so this
 * branch is a transition aid, not a contract — do not build on it.
 */
export function isEvaluationOutput(output: Output | undefined): boolean {
  if (!output) return false;
  if (output.evaluation === true) return true;

  const hasLegacyEvaluationKey =
    typeof output.lastSignalAt === "string" || typeof output.runStarted === "string";
  if (!hasLegacyEvaluationKey) return false;

  return !CALLER_OUTPUT_KEYS.some((key) => output[key] !== undefined);
}

function asString(value: unknown): string | undefined {
  return typeof value === "string" && value ? value : undefined;
}

function formatTimestamp(iso: string): string {
  const parsed = new Date(iso);
  return Number.isNaN(parsed.getTime()) ? iso : parsed.toLocaleString();
}

/**
 * signalLead renders how long before this evaluation the beat landed
 * (periodStart − lastSignalAt), reusing the same duration formatter as every
 * other elapsed-time display in the app. Returns null when either timestamp is
 * unusable or the beat is (impossibly) in the future — better to show nothing
 * than "0s before this evaluation".
 */
function signalLead(periodStart: string | undefined, lastSignalAt: string): number | null {
  if (!periodStart) return null;

  const evaluatedAt = new Date(periodStart).getTime();
  const signalAt = new Date(lastSignalAt).getTime();
  if (Number.isNaN(evaluatedAt) || Number.isNaN(signalAt)) return null;

  const lead = evaluatedAt - signalAt;
  return lead > 0 ? lead : null;
}

export function EvaluationCard({
  org,
  checkUid,
  checkType,
  output,
  periodStart,
  regionLabel,
}: {
  org: string;
  checkUid: string;
  /** The parent check's type; only "email" changes the explainer's noun. */
  checkType?: string;
  output: Output | undefined;
  /** The evaluation row's own timestamp, used for the "N before" lead. */
  periodStart?: string;
  /** Already-resolved friendly region label; omitted when the row has none. */
  regionLabel?: string;
}) {
  const { t } = useTranslation(["checks", "common"]);

  if (!isEvaluationOutput(output)) return null;

  const message = asString(output?.message);
  const lastSignalAt = asString(output?.lastSignalAt);
  const lastSignalResultUid = asString(output?.lastSignalResultUid);
  const overdueBy = asString(output?.overdueBy);
  const runStarted = asString(output?.runStarted);

  const isEmail = checkType === "email";
  const explainerKey = regionLabel
    ? isEmail
      ? "checks:resultDetail.evaluation.explainerEmail"
      : "checks:resultDetail.evaluation.explainerHeartbeat"
    : isEmail
      ? "checks:resultDetail.evaluation.explainerEmailNoRegion"
      : "checks:resultDetail.evaluation.explainerNoRegion";

  const lead = lastSignalAt ? signalLead(periodStart, lastSignalAt) : null;

  return (
    <Card data-testid="evaluation-card">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <CalendarClock className="h-4 w-4 text-muted-foreground" />
          {t("checks:resultDetail.evaluation.title")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        {message && <div className="font-medium">{message}</div>}
        <p className="text-muted-foreground">
          {t(explainerKey, { region: regionLabel })}
        </p>

        <div className="space-y-1">
          {lastSignalAt ? (
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-muted-foreground">
                {t("checks:resultDetail.evaluation.lastSignal")}:
              </span>
              <code className="font-mono" data-testid="evaluation-last-signal">
                {formatTimestamp(lastSignalAt)}
              </code>
              {lead !== null && (
                <span className="text-xs text-muted-foreground">
                  {t("checks:resultDetail.evaluation.before", {
                    duration: formatDuration(lead),
                  })}
                </span>
              )}
            </div>
          ) : (
            !runStarted && (
              <div className="text-muted-foreground" data-testid="evaluation-no-signal">
                {t("checks:resultDetail.evaluation.noSignal")}
              </div>
            )
          )}
          {runStarted && (
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-muted-foreground">
                {t("checks:resultDetail.evaluation.runStarted")}:
              </span>
              <code className="font-mono">{formatTimestamp(runStarted)}</code>
            </div>
          )}
          {overdueBy && (
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-muted-foreground">
                {t("checks:resultDetail.evaluation.overdueBy")}:
              </span>
              <code className="font-mono">{overdueBy}</code>
            </div>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          {lastSignalResultUid && (
            <Link
              to="/orgs/$org/checks/$checkUid/results/$resultUid"
              params={{ org, checkUid, resultUid: lastSignalResultUid }}
              // Deliberately DROP the region search param. The evaluation row
              // has a region; the beat it points at has none, so carrying
              // `?region=` over would scope the beat's prev/next neighbours to
              // a region it is not in and strand the reader.
              search={{ region: undefined }}
              className="inline-flex items-center gap-1 text-primary hover:underline"
              data-testid="evaluation-open-signal"
            >
              <ArrowRight className="h-3.5 w-3.5" />
              {t("checks:resultDetail.evaluation.openSignal")}
            </Link>
          )}
          <Link
            to="/orgs/$org/checks/$checkUid"
            params={{ org, checkUid }}
            search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
            className="inline-flex items-center gap-1 text-primary hover:underline"
            data-testid="evaluation-view-all"
          >
            {t("checks:resultDetail.viewAll")}
          </Link>
        </div>
      </CardContent>
    </Card>
  );
}
