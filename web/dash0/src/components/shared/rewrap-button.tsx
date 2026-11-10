import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { Loader2, RefreshCw } from "lucide-react";

import { isRewrapSettled, useRewrapStatus, useStartRewrap, type RewrapOutcome } from "@/api/hooks";
import { Button } from "@/components/ui/button";

interface RewrapButtonProps {
  org: string;
  /** Re-seal the flagged checks of this private region. */
  region?: string;
  /** Re-seal these checks only. */
  checkUids?: string[];
  /** Suffix of the test ids, when several buttons share a page. */
  testId?: string;
  /** Called once a re-wrap was started, so the caller keeps it mounted. */
  onStarted?: () => void;
}

/** Merges the outcomes of the start call with the polled statuses. */
function mergeOutcomes(started: RewrapOutcome[] | null, polled: RewrapOutcome[] | undefined): RewrapOutcome[] | null {
  if (!started) return null;

  return started.map((outcome) => {
    if (outcome.status === "refused") return outcome;
    const latest = polled?.find((entry) => entry.checkUid === outcome.checkUid && entry.taskUid === outcome.taskUid);
    return latest ? { ...outcome, ...latest, checkName: outcome.checkName ?? latest.checkName } : outcome;
  });
}

/**
 * "Re-seal for the current agents" (spec 2026-11-10-03 §1): an online agent
 * that can open the sealed secrets re-seals them for the current agents (or
 * region key). The server relays ciphertext only. Each check that could not be
 * re-sealed is listed with the reason; it keeps its old secrets and its flag.
 */
export function RewrapButton({ org, region, checkUids, testId, onStarted }: RewrapButtonProps) {
  const { t } = useTranslation(["org"]);
  const queryClient = useQueryClient();
  const start = useStartRewrap(org);
  const [started, setStarted] = useState<RewrapOutcome[] | null>(null);

  const running = useMemo(
    () => (started ?? []).filter((outcome) => outcome.status !== "refused").map((outcome) => outcome.checkUid),
    [started],
  );
  const status = useRewrapStatus(org, running);
  const results = mergeOutcomes(started, status.data);
  const settled = !!results && results.every(isRewrapSettled);
  const suffix = testId ? `-${testId}` : "";

  useEffect(() => {
    if (!settled) return;
    void queryClient.invalidateQueries({ queryKey: ["sealing-reseal", org] });
    void queryClient.invalidateQueries({ queryKey: ["private-regions", org] });
    void queryClient.invalidateQueries({ queryKey: ["check", org] });
  }, [settled, org, queryClient]);

  const run = async () => {
    setStarted(null);
    onStarted?.();
    setStarted(await start.mutateAsync({ region, checkUids }));
  };

  const busy = start.isPending || (!!results && !settled);
  const failed = (results ?? []).filter((outcome) => outcome.status !== "done" && isRewrapSettled(outcome));
  const done = (results ?? []).filter((outcome) => outcome.status === "done");

  const reason = (outcome: RewrapOutcome) =>
    t(`privateLocations.rewrap.reason.${outcome.errorCode ?? outcome.status}`, {
      defaultValue: t("privateLocations.rewrap.reason.unknown", { code: outcome.errorCode ?? outcome.status }),
    });

  return (
    <div className="space-y-1" data-testid={`rewrap${suffix}`}>
      <Button variant="outline" size="sm" onClick={run} disabled={busy} data-testid={`rewrap-button${suffix}`}>
        {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
        {t("privateLocations.rewrap.button")}
      </Button>
      {start.isError && (
        <p className="text-xs text-destructive" data-testid={`rewrap-error${suffix}`}>
          {t("privateLocations.rewrap.error")}
        </p>
      )}
      {results && results.length === 0 && (
        <p className="text-xs text-muted-foreground" data-testid={`rewrap-empty${suffix}`}>
          {t("privateLocations.rewrap.empty")}
        </p>
      )}
      {results && results.length > 0 && !settled && (
        <p className="text-xs text-muted-foreground" data-testid={`rewrap-running${suffix}`}>
          {t("privateLocations.rewrap.running", { count: results.length })}
        </p>
      )}
      {settled && done.length > 0 && (
        <p className="text-xs text-status-ok" data-testid={`rewrap-done${suffix}`}>
          {t("privateLocations.rewrap.done", { count: done.length })}
        </p>
      )}
      {settled && failed.length > 0 && (
        <ul className="space-y-0.5 text-xs text-status-warning" data-testid={`rewrap-failures${suffix}`}>
          {failed.map((outcome) => (
            <li key={outcome.checkUid} data-testid={`rewrap-failure-${outcome.checkUid}`}>
              {t("privateLocations.rewrap.failed", { check: outcome.checkName || outcome.checkUid, reason: reason(outcome) })}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
