import { useEffect, useRef, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Camera, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  useCaptureCheckScreenshot,
  useCheckScreenshots,
  type CheckScreenshot,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgo } from "@/components/ui/time-ago";
import { ScreenshotImageLink } from "@/components/shared/screenshot-image";

/** How often the card re-lists while a "Capture now" run is on its way. */
const PENDING_POLL_MS = 5_000;
/** When the card stops waiting for a requested capture. A browser run takes a
 * few seconds; the rest of the budget covers the worker picking the job up. */
const PENDING_TIMEOUT_MS = 3 * 60 * 1000;

interface PendingCapture {
  requestedAt: string;
  /** The captures already listed when the request was made: the answer is the
   * first one that is not among them. */
  knownUids: Set<string>;
}

export interface CheckScreenshotsCardProps {
  org: string;
  /** The check's route identifier (uid or slug — the API takes both). */
  checkUid: string;
  checkType: string;
  /** Whether a browser check has its failure-screenshot option on. */
  screenshotEnabled: boolean;
}

/**
 * CheckScreenshotsCard shows a check's latest screenshots on the check page
 * (spec 2026-09-25-34): the newest capture as a thumbnail that opens full size,
 * when and where it was taken, the incident it belongs to, and the older ones
 * in a strip. "Capture now" runs the check once with the capture forced.
 *
 * Like the incident card it is careful about what it claims: a capture of a
 * failing run is what the page looked like a moment AFTER the check decided it
 * was unhealthy, which the description says.
 */
export function CheckScreenshotsCard({
  org,
  checkUid,
  checkType,
  screenshotEnabled,
}: CheckScreenshotsCardProps) {
  const { t } = useTranslation("checks");
  const [pending, setPending] = useState<PendingCapture | null>(null);
  const [expired, setExpired] = useState(false);
  const announced = useRef<string | null>(null);

  const listing = useCheckScreenshots(org, checkUid);
  const shots = listing.data ?? [];

  const arrived =
    pending !== null && shots.some((shot) => !pending.knownUids.has(shot.uid));
  const waiting = pending !== null && !arrived && !expired;

  // Re-list on a short interval only while a requested capture is on its way.
  useCheckScreenshots(org, checkUid, {
    enabled: waiting,
    pollMs: PENDING_POLL_MS,
  });

  const capture = useCaptureCheckScreenshot(org, checkUid);

  useEffect(() => {
    if (!pending) return;
    const timer = window.setTimeout(() => setExpired(true), PENDING_TIMEOUT_MS);
    return () => window.clearTimeout(timer);
  }, [pending]);

  useEffect(() => {
    if (!pending || announced.current === pending.requestedAt) return;
    if (arrived) {
      announced.current = pending.requestedAt;
      toast.success(t("detail.screenshots.captured"));
    } else if (expired) {
      announced.current = pending.requestedAt;
      toast.info(t("detail.screenshots.stillWaiting"));
    }
  }, [arrived, expired, pending, t]);

  const onCaptureNow = () => {
    const knownUids = new Set(shots.map((shot) => shot.uid));

    capture.mutate(undefined, {
      onSuccess: (resp) => {
        setExpired(false);
        setPending({ requestedAt: resp.requestedAt, knownUids });
        toast.success(
          resp.region
            ? t("detail.screenshots.requestedFrom", { region: resp.region })
            : t("detail.screenshots.requested"),
        );
      },
      onError: (err) => toast.error(captureErrorMessage(err, t)),
    });
  };

  const [latest, ...older] = shots;

  return (
    <Card data-testid="check-screenshots-card">
      <CardHeader className="flex flex-col gap-3 space-y-0 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1.5">
          <CardTitle>{t("detail.screenshots.title")}</CardTitle>
          <CardDescription>{t("detail.screenshots.description")}</CardDescription>
        </div>
        <Button
          variant="outline"
          size="sm"
          className="w-full shrink-0 sm:w-auto"
          onClick={onCaptureNow}
          disabled={capture.isPending || waiting}
          data-testid="check-screenshots-capture-now"
        >
          {capture.isPending || waiting ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Camera className="h-4 w-4" />
          )}
          {waiting ? t("detail.screenshots.capturing") : t("detail.screenshots.captureNow")}
        </Button>
      </CardHeader>
      <CardContent className="space-y-4">
        {waiting && (
          <p
            className="text-sm text-muted-foreground"
            data-testid="check-screenshots-pending"
          >
            {t("detail.screenshots.pending")}
          </p>
        )}

        {listing.isLoading ? (
          <Skeleton className="aspect-video w-full" />
        ) : latest ? (
          <LatestScreenshot org={org} shot={latest} />
        ) : (
          <EmptyScreenshots
            org={org}
            checkUid={checkUid}
            checkType={checkType}
            screenshotEnabled={screenshotEnabled}
          />
        )}

        {older.length > 0 && (
          <ul
            className="grid grid-cols-2 gap-2 sm:grid-cols-4"
            aria-label={t("detail.screenshots.older")}
            data-testid="check-screenshots-strip"
          >
            {older.map((shot) => (
              <li key={shot.uid}>
                <ScreenshotImageLink
                  src={shot.downloadUrl}
                  alt={t("detail.screenshots.alt")}
                  title={thumbnailTitle(shot, t)}
                  className="aspect-video"
                  imgClassName="h-full w-full object-cover object-top"
                  data-testid="check-screenshots-thumbnail"
                />
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

type TFn = ReturnType<typeof useTranslation>["t"];

function LatestScreenshot({ org, shot }: { org: string; shot: CheckScreenshot }) {
  const { t } = useTranslation("checks");

  return (
    <figure className="space-y-2" data-testid="check-screenshot-latest">
      <ScreenshotImageLink
        src={shot.downloadUrl}
        alt={t("detail.screenshots.alt")}
        imageTestId="check-screenshot-image"
      />
      <figcaption
        className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground"
        data-testid="check-screenshot-caption"
      >
        <TimeAgo date={shot.capturedAt} data-testid="check-screenshot-time" />
        <span data-testid="check-screenshot-region">
          {shot.region || t("detail.screenshots.unknownRegion")}
        </span>
        <span data-testid="check-screenshot-trigger">{triggerLabel(shot.trigger, t)}</span>
        {shot.incidentUid && (
          <Link
            to="/orgs/$org/incidents/$incidentUid"
            params={{ org, incidentUid: shot.incidentUid }}
            className="font-medium text-primary hover:underline"
            data-testid="check-screenshot-incident-link"
          >
            {t("detail.screenshots.viewIncident")}
          </Link>
        )}
      </figcaption>
    </figure>
  );
}

function EmptyScreenshots({
  org,
  checkUid,
  checkType,
  screenshotEnabled,
}: {
  org: string;
  checkUid: string;
  checkType: string;
  screenshotEnabled: boolean;
}) {
  const { t } = useTranslation("checks");

  return (
    <div
      className="space-y-2 rounded-md border border-dashed p-4 text-sm text-muted-foreground"
      data-testid="check-screenshots-empty"
    >
      <p>{t("detail.screenshots.empty")}</p>
      {checkType === "browser" && !screenshotEnabled && (
        <Link
          to="/orgs/$org/checks/$checkUid/edit"
          params={{ org, checkUid }}
          search={{ section: "browser-screenshot" }}
          className="inline-block font-medium text-primary hover:underline"
          data-testid="check-screenshots-enable-link"
        >
          {t("detail.screenshots.enableOnFailure")}
        </Link>
      )}
      {checkType === "js" && <p>{t("detail.screenshots.jsHint")}</p>}
    </div>
  );
}

function triggerLabel(trigger: CheckScreenshot["trigger"], t: TFn): string {
  switch (trigger) {
    case "incident-open":
      return t("detail.screenshots.triggers.incidentOpen");
    case "incident-reopen":
      return t("detail.screenshots.triggers.incidentReopen");
    case "check-failure":
      return t("detail.screenshots.triggers.checkFailure");
    case "capture-now":
      return t("detail.screenshots.triggers.captureNow");
    case "agent-upload":
      return t("detail.screenshots.triggers.agentUpload");
    default:
      return t("detail.screenshots.triggers.unknown");
  }
}

function thumbnailTitle(shot: CheckScreenshot, t: TFn): string {
  const when = new Date(shot.capturedAt).toLocaleString();
  return shot.region
    ? t("detail.screenshots.thumbnailTitle", { capturedAt: when, region: shot.region })
    : when;
}

function captureErrorMessage(err: unknown, t: TFn): string {
  if (err instanceof ApiError) {
    if (err.status === 429) {
      return err.retryAfter
        ? t("detail.screenshots.rateLimited", { seconds: err.retryAfter })
        : t("detail.screenshots.rateLimitedNoDelay");
    }
    if (err.status === 403) return t("detail.screenshots.forbidden");
    if (err.status === 409) return t("detail.screenshots.noScheduledRun");
  }
  return t("detail.screenshots.captureFailed");
}
