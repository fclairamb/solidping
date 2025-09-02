import { useState, useEffect } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { AlertCircle, Check, Loader2 } from "lucide-react";
import { ApiError } from "@/api/client";
import { useSystemParameters, useSetSystemParameter } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/performance")({
  component: PerformanceSettingsPage,
});

function PerformanceSettingsPage() {
  const { data: params, isLoading } = useSystemParameters();
  const setParam = useSetSystemParameter();

  const [checkWorkers, setCheckWorkers] = useState("3");
  const [jobWorkers, setJobWorkers] = useState("2");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (params) {
      const get = (key: string) => params.find((p) => p.key === key)?.value;
      const cw = get("check_workers");
      const jw = get("job_workers");
      if (cw != null) setCheckWorkers(String(cw));
      if (jw != null) setJobWorkers(String(jw));
    }
  }, [params]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      await Promise.all([
        setParam.mutateAsync({
          key: "check_workers",
          value: parseInt(checkWorkers, 10),
        }),
        setParam.mutateAsync({
          key: "job_workers",
          value: parseInt(jobWorkers, 10),
        }),
      ]);
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError("An unexpected error occurred");
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
    <Card>
      <CardHeader>
        <CardTitle>Performance</CardTitle>
        <CardDescription>
          Configure the number of concurrent workers for checks and jobs.
          Changes take effect after server restart.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSave} className="space-y-4">
          {error && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          {saved && (
            <Alert>
              <Check className="h-4 w-4" />
              <AlertDescription>Settings saved.</AlertDescription>
            </Alert>
          )}

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="checkWorkers">Check Runners</Label>
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
                Number of concurrent goroutines for running health checks.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="jobWorkers">Job Runners</Label>
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
                Number of concurrent goroutines for processing background jobs.
              </p>
            </div>
          </div>

          <Button type="submit" disabled={setParam.isPending}>
            {setParam.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                Saving...
              </>
            ) : (
              "Save"
            )}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
