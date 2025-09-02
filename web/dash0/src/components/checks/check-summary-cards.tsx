import { useState, useEffect } from "react";
import { AlertTriangle, ArrowUp, ArrowDown, Clock } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import type { Check } from "@/api/hooks";

interface CheckSummaryCardsProps {
  check: Check;
  totalIncidents: number;
}

function formatDuration(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return `${days}d ${hours % 24}h ${minutes % 60}m`;
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`;
  return `${seconds}s`;
}

function LiveDuration({ since }: { since: string }) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const interval = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(interval);
  }, []);

  const elapsed = now - new Date(since).getTime();
  return <>{formatDuration(Math.max(0, elapsed))}</>;
}

export function CheckSummaryCards({
  check,
  totalIncidents,
}: CheckSummaryCardsProps) {
  const isUp = check.lastResult?.status === "up";
  const isDown =
    check.lastResult?.status === "down" ||
    check.lastResult?.status === "error" ||
    check.lastResult?.status === "timeout";

  return (
    <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
      {/* Uptime / Downtime card */}
      <Card>
        <CardContent className="pt-6">
          <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
            {isUp ? (
              <ArrowUp className="h-4 w-4 text-green-500" />
            ) : isDown ? (
              <ArrowDown className="h-4 w-4 text-red-500" />
            ) : (
              <ArrowUp className="h-4 w-4" />
            )}
            {isUp ? "Currently up for" : isDown ? "Currently down for" : "Status"}
          </div>
          <div className="text-2xl font-bold">
            {check.lastStatusChange?.time ? (
              <LiveDuration since={check.lastStatusChange.time} />
            ) : (
              "Unknown"
            )}
          </div>
        </CardContent>
      </Card>

      {/* Last checked card */}
      <Card>
        <CardContent className="pt-6">
          <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
            <Clock className="h-4 w-4" />
            Last checked
          </div>
          <div className="text-2xl font-bold">
            {check.lastResult?.timestamp ? (
              <><LiveDuration since={check.lastResult.timestamp} /> ago</>
            ) : (
              "Never"
            )}
          </div>
        </CardContent>
      </Card>

      {/* Incidents card */}
      <Card data-testid="incidents-card">
        <CardContent className="pt-6">
          <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
            <AlertTriangle className="h-4 w-4" />
            Incidents
          </div>
          <div className="text-2xl font-bold">{totalIncidents}</div>
        </CardContent>
      </Card>
    </div>
  );
}
