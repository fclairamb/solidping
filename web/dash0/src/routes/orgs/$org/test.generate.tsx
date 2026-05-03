import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useState } from "react";
import { Loader2, Plus } from "lucide-react";
import { useGenerateData } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export const Route = createFileRoute("/orgs/$org/test/generate")({
  component: GenerateDataTab,
});

function formatDefaultStartDate(daysAgo: number): string {
  const d = new Date();
  d.setDate(d.getDate() - daysAgo);
  return d.toISOString().slice(0, 10);
}

function GenerateDataTab() {
  const { t } = useTranslation("nav");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const generateData = useGenerateData();

  const [name, setName] = useState("Generated Check");
  const [checkPeriodSec, setCheckPeriodSec] = useState("1545");
  const [startDate, setStartDate] = useState(formatDefaultStartDate(7));
  const [failureRate, setFailureRate] = useState("0");
  const [failureBurstSec, setFailureBurstSec] = useState("0");
  const [avgDurationMs, setAvgDurationMs] = useState("150");

  const handleGenerate = async () => {
    try {
      const result = await generateData.mutateAsync({
        org,
        name,
        checkPeriodSec: Number(checkPeriodSec),
        startDate,
        failureRate: Number(failureRate),
        failureBurstSec: Number(failureBurstSec),
        avgDurationMs: Number(avgDurationMs),
      });
      toast.success(t("test.generate.createdResults", { count: result.resultsCount }), {
        action: {
          label: t("test.generate.viewAction"),
          onClick: () =>
            navigate({
              to: "/orgs/$org/checks/$checkUid",
              params: { org, checkUid: result.checkSlug },
              search: { graphPeriod: undefined, graphFull: undefined },
            }),
        },
      });
    } catch (err) {
      const message =
        err instanceof Error ? err.message : t("test.generate.generateFailed");
      toast.error(message);
    }
  };

  const periodSec = Number(checkPeriodSec) || 60;
  const start = new Date(startDate);
  const now = new Date();
  const estimatedResults = Math.max(
    0,
    Math.floor((now.getTime() - start.getTime()) / 1000 / periodSec),
  );

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("test.generate.title")}</CardTitle>
          <CardDescription>{t("test.generate.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            <div className="space-y-2">
              <Label htmlFor="gen-name">{t("test.generate.checkName")}</Label>
              <Input
                id="gen-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-start">{t("test.generate.startDate")}</Label>
              <Input
                id="gen-start"
                type="date"
                value={startDate}
                onChange={(e) => setStartDate(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-period">{t("test.generate.checkPeriod")}</Label>
              <Input
                id="gen-period"
                type="number"
                min="1"
                max="3600"
                value={checkPeriodSec}
                onChange={(e) => setCheckPeriodSec(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("test.generate.checkPeriodHelp")}
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-duration">{t("test.generate.avgDuration")}</Label>
              <Input
                id="gen-duration"
                type="number"
                min="1"
                max="30000"
                value={avgDurationMs}
                onChange={(e) => setAvgDurationMs(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("test.generate.avgDurationHelp")}
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-failure-rate">{t("test.generate.failureRate")}</Label>
              <Input
                id="gen-failure-rate"
                type="number"
                min="0"
                max="1"
                step="0.01"
                value={failureRate}
                onChange={(e) => setFailureRate(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("test.generate.failureRateHelp")}
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-burst">{t("test.generate.failureBurst")}</Label>
              <Input
                id="gen-burst"
                type="number"
                min="0"
                max="86400"
                value={failureBurstSec}
                onChange={(e) => setFailureBurstSec(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("test.generate.failureBurstHelp")}
              </p>
            </div>
          </div>

          <div className="mt-4 text-sm text-muted-foreground">
            {t("test.generate.estimatedResults")}{" "}
            <span className="font-mono font-medium text-foreground">
              {estimatedResults.toLocaleString()}
            </span>
          </div>
        </CardContent>
        <CardFooter>
          <Button
            onClick={handleGenerate}
            disabled={
              generateData.isPending || !startDate || estimatedResults === 0
            }
          >
            {generateData.isPending ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <Plus className="mr-2 h-4 w-4" />
            )}
            {t("test.generate.generate")}
          </Button>
        </CardFooter>
      </Card>
    </div>
  );
}
