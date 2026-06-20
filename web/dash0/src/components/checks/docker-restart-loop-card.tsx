import { useTranslation } from "react-i18next";
import { Activity, AlertTriangle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

type Output = Record<string, unknown>;

function asNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function formatSeconds(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const m = Math.floor(seconds / 60);
  const s = Math.round(seconds % 60);
  return s > 0 ? `${m}m ${s}s` : `${m}m`;
}

// DockerRestartLoopCard surfaces the restart-loop heuristic of a Docker check's
// last result: the lifetime restart count, time since the current run started,
// and an amber "restart loop" badge when the backend flagged restartLoop. Only
// renders when the output carries restart-loop context (restartCount or
// secondsSinceStart), so it is safe to mount unconditionally for Docker checks.
export function DockerRestartLoopCard({ output }: { output: Output | undefined }) {
  const { t } = useTranslation(["checks", "common"]);

  const restartCount = asNumber(output?.restartCount);
  const secondsSinceStart = asNumber(output?.secondsSinceStart);
  const looping = output?.restartLoop === true;

  // Nothing useful to show unless detection ran (secondsSinceStart) or we at
  // least have a restart count to report.
  if (restartCount === undefined && secondsSinceStart === undefined) return null;

  return (
    <Card data-testid="docker-restart-loop-card">
      <CardHeader>
        <div className="flex flex-wrap items-center gap-2">
          <CardTitle>{t("checks:docker.restartLoopTitle", "Restart activity")}</CardTitle>
          {looping ? (
            <Badge variant="warning" className="gap-1" data-testid="docker-restart-loop-badge">
              <AlertTriangle className="h-3 w-3" />
              {t("checks:docker.restartLoopDetected", "Restart loop suspected")}
            </Badge>
          ) : (
            <Badge variant="success" className="gap-1">
              <Activity className="h-3 w-3" />
              {t("checks:docker.restartLoopStable", "Stable")}
            </Badge>
          )}
        </div>
        <CardDescription>
          {t(
            "checks:docker.restartLoopDescription",
            "A running container that restarts repeatedly within the recency window is flagged as a suspected crash-loop (Warning).",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3">
          <div>
            <dt className="text-sm text-muted-foreground">
              {t("checks:docker.restartCount", "Restart count")}
            </dt>
            <dd className="text-2xl font-bold font-mono">
              {restartCount !== undefined ? restartCount : "-"}
            </dd>
          </div>
          <div>
            <dt className="text-sm text-muted-foreground">
              {t("checks:docker.secondsSinceStart", "Since last start")}
            </dt>
            <dd className="text-2xl font-bold font-mono">
              {secondsSinceStart !== undefined ? formatSeconds(secondsSinceStart) : "-"}
            </dd>
          </div>
        </dl>
      </CardContent>
    </Card>
  );
}
