import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { Loader2, Play } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { type RunCheckNowResponse, useResults, useRunCheckNow } from "@/api/hooks";
import { Button } from "@/components/ui/button";

/** How often the results are re-read while a requested run is on its way. */
const PENDING_POLL_MS = 2_000;

/** Give up waiting after this long: the run may still land, the button frees. */
const PENDING_TIMEOUT_MS = 2 * 60 * 1000;

type TFn = ReturnType<typeof useTranslation>["t"];

interface RunNowButtonProps {
  org: string;
  checkUid: string;
}

/**
 * "Run now" (spec 2026-10-04-01): runs the check once in every region. The
 * button spins until a result with `periodStart >= requestedAt` has arrived for
 * each region that was queued (counted, as a result of a region-less check carries no region name) (a region reported `running` is answered by its
 * current run, which may have started earlier, so it is not waited on), then
 * toasts the new status. The results are only polled while a run is pending.
 */
export function RunNowButton({ org, checkUid }: RunNowButtonProps) {
  const { t } = useTranslation("checks");
  const run = useRunCheckNow(org, checkUid);
  const [pending, setPending] = useState<RunCheckNowResponse | null>(null);

  const onClick = () => {
    run.mutate(undefined, {
      onSuccess: (resp) => {
        setPending(resp);
        toast.success(t("detail.runNow.requested"));
      },
      onError: (err) => toast.error(runNowErrorMessage(err, t)),
    });
  };

  const busy = run.isPending || pending !== null;

  return (
    <>
      {pending && (
        <RunNowWatcher
          org={org}
          checkUid={checkUid}
          request={pending}
          onSettled={() => setPending(null)}
        />
      )}
      <Button
        variant="outline"
        size="icon"
        className="md:h-9 md:w-auto md:px-4 md:py-2"
        aria-label={t("detail.runNow.button")}
        disabled={busy}
        onClick={onClick}
        data-testid="check-run-now"
      >
        {busy ? (
          <Loader2 className="h-4 w-4 animate-spin md:mr-2" />
        ) : (
          <Play className="h-4 w-4 md:mr-2" />
        )}
        <span className="hidden md:inline">
          {pending ? t("detail.runNow.running") : t("detail.runNow.button")}
        </span>
      </Button>
    </>
  );
}

interface RunNowWatcherProps {
  org: string;
  checkUid: string;
  request: RunCheckNowResponse;
  onSettled: () => void;
}

/** Polls the results of one pending request, toasts the outcome and settles. */
function RunNowWatcher({ org, checkUid, request, onSettled }: RunNowWatcherProps) {
  const { t } = useTranslation("checks");
  const queryClient = useQueryClient();
  const settled = useRef(false);

  const { data: answers } = useResults(org, {
    checkUid,
    periodType: "raw",
    periodStartAfter: request.requestedAt,
    size: 50,
    refetchInterval: PENDING_POLL_MS,
  });

  const requestedMs = Date.parse(request.requestedAt);
  const fresh = (answers?.data ?? []).filter(
    (result) =>
      result.periodStart !== undefined &&
      Date.parse(result.periodStart) >= requestedMs &&
      (result.status === "up" || result.status === "down"),
  );
  // One fresh result per queued region. Counted, not matched by name: a
  // result's region is null for a check without a named region.
  const queued = request.regions.filter((r) => r.status === "queued").length;
  const arrived = fresh.length >= Math.max(queued, 1);
  const down = fresh.some((result) => result.status === "down");

  useEffect(() => {
    if (!arrived || settled.current) return;
    settled.current = true;
    toast.success(t("detail.runNow.done", { status: down ? "down" : "up" }));
    void queryClient.invalidateQueries({ queryKey: ["results", org] });
    void queryClient.invalidateQueries({ queryKey: ["incidents", org] });
    onSettled();
  }, [arrived, down, onSettled, org, queryClient, t]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      if (settled.current) return;
      settled.current = true;
      toast.info(t("detail.runNow.stillWaiting"));
      onSettled();
    }, PENDING_TIMEOUT_MS);
    return () => window.clearTimeout(timer);
  }, [onSettled, t]);

  return null;
}

function runNowErrorMessage(err: unknown, t: TFn): string {
  if (err instanceof ApiError) {
    if (err.status === 429) {
      return err.retryAfter
        ? t("detail.runNow.rateLimited", { seconds: err.retryAfter })
        : t("detail.runNow.rateLimitedNoDelay");
    }
    if (err.status === 403) return t("detail.runNow.forbidden");
    if (err.status === 409) return t("detail.runNow.noScheduledRun");
  }
  return t("detail.runNow.failed");
}
