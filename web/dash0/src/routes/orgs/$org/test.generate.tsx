import { createFileRoute, useNavigate } from "@tanstack/react-router";
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
      toast.success(`Created check with ${result.resultsCount} results`, {
        action: {
          label: "View",
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
        err instanceof Error ? err.message : "Failed to generate data";
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
          <CardTitle className="text-base">Generate historical data</CardTitle>
          <CardDescription>
            Create an HTTP check (1.1.1.1) and backfill it with simulated
            results from a start date up to now.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            <div className="space-y-2">
              <Label htmlFor="gen-name">Check name</Label>
              <Input
                id="gen-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-start">Start date</Label>
              <Input
                id="gen-start"
                type="date"
                value={startDate}
                onChange={(e) => setStartDate(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-period">Check period (seconds)</Label>
              <Input
                id="gen-period"
                type="number"
                min="1"
                max="3600"
                value={checkPeriodSec}
                onChange={(e) => setCheckPeriodSec(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Interval between each simulated result
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-duration">Avg response time (ms)</Label>
              <Input
                id="gen-duration"
                type="number"
                min="1"
                max="30000"
                value={avgDurationMs}
                onChange={(e) => setAvgDurationMs(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Randomized with ~20% standard deviation
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-failure-rate">Failure rate (0-1)</Label>
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
                0 = no failures, 0.05 = 5% failure rate
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="gen-burst">Failure burst duration (s)</Label>
              <Input
                id="gen-burst"
                type="number"
                min="0"
                max="86400"
                value={failureBurstSec}
                onChange={(e) => setFailureBurstSec(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                0 = random failures, &gt;0 = consecutive failure bursts of this
                duration
              </p>
            </div>
          </div>

          <div className="mt-4 text-sm text-muted-foreground">
            Estimated results:{" "}
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
            Generate
          </Button>
        </CardFooter>
      </Card>
    </div>
  );
}
