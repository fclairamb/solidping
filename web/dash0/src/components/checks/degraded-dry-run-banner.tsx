import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useUpdateCheck, type Check } from "@/api/hooks";

interface DegradedDryRunBannerProps {
  org: string;
  check: Check;
  /**
   * Chart window to link into — the hour leading up to the stamp. Absent when
   * there is no stamp to link to.
   */
  windowUrl?: { graphFrom: number; graphTo: number };
}

/**
 * DegradedDryRunBanner is the adoption path for degraded detection (spec
 * 2026-09-22-03).
 *
 * The feature ships OFF for every check that predates it, because upgrading must
 * never start notifying on its own. The evaluator still runs on those checks as
 * a DRY RUN: it opens nothing and only records when it would have fired. Without
 * this banner that recording reaches nobody, and the feature is a column no
 * operator ever turns on.
 *
 * Renders nothing when there is no stamp, or when the check is already enabled
 * (the evaluator clears the stamp on the next sweep after enabling, and this
 * guard covers the seconds in between).
 */
export function DegradedDryRunBanner({
  org,
  check,
  windowUrl,
}: DegradedDryRunBannerProps) {
  const { t } = useTranslation(["checks", "common"]);
  const updateCheck = useUpdateCheck(org, check.uid ?? "");
  const [enabling, setEnabling] = useState(false);

  if (!check.degradedWouldFireAt || check.degradedEnabled) {
    return null;
  }

  const firedAt = new Date(check.degradedWouldFireAt);
  const when = Number.isNaN(firedAt.getTime())
    ? check.degradedWouldFireAt
    : firedAt.toLocaleString();

  const enable = async () => {
    setEnabling(true);
    try {
      await updateCheck.mutateAsync({ degradedEnabled: true });
      toast.success(t("checks:detail.degraded.enabled"));
    } catch {
      toast.error(t("checks:detail.degraded.enableFailed"));
    } finally {
      setEnabling(false);
    }
  };

  return (
    <Alert variant="warning" data-testid="degraded-dry-run-banner">
      <AlertTriangle />
      <AlertTitle>{t("checks:detail.degraded.bannerTitle")}</AlertTitle>
      <AlertDescription className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <span>
          {t("checks:detail.degraded.bannerDescription", { when })}
          {windowUrl && check.uid ? (
            <>
              {" "}
              <Link
                to="/orgs/$org/checks/$checkUid"
                params={{ org, checkUid: check.uid }}
                search={(prev) => ({ ...prev, ...windowUrl })}
                className="underline"
                data-testid="degraded-dry-run-window-link"
              >
                {t("checks:detail.degraded.bannerWindowLink")}
              </Link>
            </>
          ) : null}
        </span>
        <Button
          size="sm"
          variant="outline"
          onClick={enable}
          disabled={enabling}
          data-testid="degraded-enable-button"
          className="shrink-0"
        >
          {enabling ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            t("checks:detail.degraded.enableAction")
          )}
        </Button>
      </AlertDescription>
    </Alert>
  );
}
