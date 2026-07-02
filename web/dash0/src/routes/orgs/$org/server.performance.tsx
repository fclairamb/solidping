import { useState, useEffect } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { AlertCircle, Check, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { ApiError } from "@/api/client";
import {
  useSystemParameters,
  useSetSystemParameter,
  useSchedulingLaneLoad,
  type WorkerLaneLoad,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/performance")({
  component: PerformanceSettingsPage,
});

function PerformanceSettingsPage() {
  const { t } = useTranslation(["server", "common"]);
  const { data: params, isLoading } = useSystemParameters();
  const setParam = useSetSystemParameter();

  const [checkWorkers, setCheckWorkers] = useState("25");
  const [jobWorkers, setJobWorkers] = useState("2");
  const [fastLaneReserved, setFastLaneReserved] = useState("5");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (params) {
      const get = (key: string) => params.find((p) => p.key === key)?.value;
      const cw = get("server.check_workers");
      const jw = get("server.job_workers");
      const fl = get("scheduling.fast_lane_reserved");
      if (cw != null) setCheckWorkers(String(cw));
      if (jw != null) setJobWorkers(String(jw));
      if (fl != null) setFastLaneReserved(String(fl));
    }
  }, [params]);

  // The backend clamps the floor to [0, pool_size − 1]; surface the same
  // constraint here so a save that would be silently clamped is caught.
  const checkWorkersNum = parseInt(checkWorkers, 10);
  const fastLaneReservedNum = parseInt(fastLaneReserved, 10);
  const fastLaneInvalid =
    Number.isFinite(checkWorkersNum) &&
    Number.isFinite(fastLaneReservedNum) &&
    fastLaneReservedNum >= checkWorkersNum;

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (fastLaneInvalid) return;
    setError(null);
    setSaved(false);

    try {
      await Promise.all([
        setParam.mutateAsync({
          key: "server.check_workers",
          value: parseInt(checkWorkers, 10),
        }),
        setParam.mutateAsync({
          key: "server.job_workers",
          value: parseInt(jobWorkers, 10),
        }),
        setParam.mutateAsync({
          key: "scheduling.fast_lane_reserved",
          value: parseInt(fastLaneReserved, 10),
        }),
      ]);
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("server:unexpectedError"));
      }
    }
  };

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>{t("server:performance.title")}</CardTitle>
          <CardDescription>{t("server:performance.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSave} className="space-y-6">
            {error && (
              <Alert variant="destructive">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            {saved && (
              <Alert>
                <Check className="h-4 w-4" />
                <AlertDescription>{t("server:saved")}</AlertDescription>
              </Alert>
            )}

            {/* Check runners: the pool size and the fast-lane floor carved
                out of that same pool belong together. */}
            <div className="space-y-4">
              <div>
                <h3 className="text-sm font-semibold">
                  {t("server:performance.checkRunnersGroup")}
                </h3>
                <p className="text-xs text-muted-foreground">
                  {t("server:performance.checkRunnersGroupHelp")}
                </p>
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label htmlFor="checkWorkers">
                    {t("server:performance.checkRunners")}
                  </Label>
                  <Input
                    id="checkWorkers"
                    type="number"
                    min="1"
                    max="100"
                    value={checkWorkers}
                    onChange={(e) => setCheckWorkers(e.target.value)}
                    disabled={setParam.isPending}
                  />
                  <p className="text-xs text-muted-foreground">
                    {t("server:performance.checkRunnersHelp")}
                  </p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="fastLaneReserved">
                    {t("server:performance.fastLaneReserved")}
                  </Label>
                  <Input
                    id="fastLaneReserved"
                    type="number"
                    min="0"
                    max="99"
                    value={fastLaneReserved}
                    onChange={(e) => setFastLaneReserved(e.target.value)}
                    disabled={setParam.isPending}
                    aria-invalid={fastLaneInvalid}
                    className={cn(fastLaneInvalid && "border-destructive")}
                    data-testid="fast-lane-reserved-input"
                  />
                  {fastLaneInvalid ? (
                    <p
                      className="text-xs text-destructive"
                      data-testid="fast-lane-reserved-error"
                    >
                      {t("server:performance.fastLaneReservedTooHigh")}
                    </p>
                  ) : (
                    <p className="text-xs text-muted-foreground">
                      {t("server:performance.fastLaneReservedHelp")}
                    </p>
                  )}
                </div>
              </div>
            </div>

            {/* Background job runners are a separate pool. */}
            <div className="space-y-4">
              <div>
                <h3 className="text-sm font-semibold">
                  {t("server:performance.jobRunnersGroup")}
                </h3>
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label htmlFor="jobWorkers">
                    {t("server:performance.jobRunners")}
                  </Label>
                  <Input
                    id="jobWorkers"
                    type="number"
                    min="1"
                    max="100"
                    value={jobWorkers}
                    onChange={(e) => setJobWorkers(e.target.value)}
                    disabled={setParam.isPending}
                  />
                  <p className="text-xs text-muted-foreground">
                    {t("server:performance.jobRunnersHelp")}
                  </p>
                </div>
              </div>
            </div>

            <Button type="submit" disabled={setParam.isPending || fastLaneInvalid}>
              {setParam.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("common:saving")}
                </>
              ) : (
                t("common:save")
              )}
            </Button>
          </form>
        </CardContent>
      </Card>

      <LaneLoadCard
        poolSize={Number.isFinite(checkWorkersNum) ? checkWorkersNum : 0}
        fastReserved={
          Number.isFinite(fastLaneReservedNum) ? fastLaneReservedNum : 0
        }
      />
    </div>
  );
}

// LaneLoadCard renders the server-computed per-worker lane load: how many
// jobs each lane offers a worker, the summed cost/delay EWMAs, and the duty
// sum — 100% ≈ one permanently occupied runner slot, so comparing a lane's
// duty against its slot capacity says at a glance whether the worker is
// overloaded.
function LaneLoadCard({
  poolSize,
  fastReserved,
}: {
  poolSize: number;
  fastReserved: number;
}) {
  const { t } = useTranslation(["server"]);
  const { data: workers, isLoading } = useSchedulingLaneLoad();

  const seconds = (ms: number) =>
    ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`;

  const laneRows = (w: WorkerLaneLoad) => {
    const slowCapacityPct = Math.max(0, poolSize - fastReserved) * 100;
    const fastCapacityPct = poolSize * 100;

    return [
      {
        key: "fast",
        label: t("server:performance.laneFast"),
        stats: w.fast,
        capacityPct: fastCapacityPct,
      },
      {
        key: "slow",
        label: t("server:performance.laneSlow"),
        stats: w.slow,
        capacityPct: slowCapacityPct,
      },
    ];
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("server:performance.laneLoadTitle")}</CardTitle>
        <CardDescription>
          {t("server:performance.laneLoadDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="flex items-center justify-center py-6">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </div>
        ) : !workers || workers.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("server:performance.laneLoadEmpty")}
          </p>
        ) : (
          <div className="space-y-6">
            {workers.map((w) => (
              <div key={w.workerUid} className="space-y-2">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{w.name}</span>
                  {w.region && <Badge variant="outline">{w.region}</Badge>}
                </div>
                <div className="rounded-md border overflow-x-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t("server:performance.laneCol")}</TableHead>
                        <TableHead className="text-right">
                          {t("server:performance.jobsCol")}
                        </TableHead>
                        <TableHead className="text-right">
                          {t("server:performance.costSumCol")}
                        </TableHead>
                        <TableHead className="text-right">
                          {t("server:performance.delaySumCol")}
                        </TableHead>
                        <TableHead className="text-right">
                          {t("server:performance.dutyCol")}
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {laneRows(w).map((row) => {
                        const overloaded =
                          row.capacityPct > 0 &&
                          row.stats.dutySumPct > row.capacityPct;

                        return (
                          <TableRow key={row.key} data-testid={`lane-load-${row.key}`}>
                            <TableCell>{row.label}</TableCell>
                            <TableCell className="text-right">
                              {row.stats.jobs}
                            </TableCell>
                            <TableCell className="text-right">
                              {seconds(row.stats.costEwmaSumMs)}
                            </TableCell>
                            <TableCell className="text-right">
                              {seconds(row.stats.delayEwmaSumMs)}
                            </TableCell>
                            <TableCell className="text-right">
                              <span
                                className={cn(
                                  overloaded && "font-semibold text-destructive",
                                )}
                              >
                                {Math.round(row.stats.dutySumPct)}%
                              </span>
                              {overloaded && (
                                <Badge variant="destructive" className="ml-2">
                                  {t("server:performance.overloaded")}
                                </Badge>
                              )}
                            </TableCell>
                          </TableRow>
                        );
                      })}
                    </TableBody>
                  </Table>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
