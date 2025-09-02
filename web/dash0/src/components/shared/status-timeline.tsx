import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

interface Result {
  uid: string;
  status: string;
  periodStart: string;
  durationMs?: number;
}

interface ResultsResponse {
  data: Result[];
}

async function fetchResults(
  org: string,
  checkUid: string
): Promise<ResultsResponse> {
  const response = await fetch(
    `/api/v1/orgs/${org}/results?checkUid=${checkUid}&limit=48`
  );
  if (!response.ok) {
    throw new Error("Failed to fetch results");
  }
  return response.json();
}

function StatusBar({
  status,
  timestamp,
  latency,
}: {
  status: string;
  timestamp: string;
  latency?: number;
}) {
  const bgClass =
    status === "ok" || status === "up"
      ? "bg-green-500"
      : status === "warning"
        ? "bg-yellow-500"
        : status === "error" || status === "down"
          ? "bg-red-500"
          : "bg-gray-300";

  const dotClass =
    status === "ok" || status === "up"
      ? "bg-green-400"
      : status === "warning"
        ? "bg-yellow-400"
        : status === "error" || status === "down"
          ? "bg-red-400"
          : "bg-gray-400";

  const date = new Date(timestamp);
  const timeStr = date.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          className={cn(
            "h-6 w-1.5 rounded-sm transition-all hover:scale-y-125 cursor-pointer",
            bgClass
          )}
        />
      </TooltipTrigger>
      <TooltipContent className="bg-gray-900 text-white border-gray-700">
        <div className="text-xs space-y-0.5">
          <p className="font-medium capitalize flex items-center gap-1.5">
            <span className={cn("inline-block h-2 w-2 rounded-full", dotClass)} />
            {status}
          </p>
          <p className="text-gray-400">{timeStr}</p>
          {latency !== undefined && (
            <p className="text-gray-400">{Math.round(latency)}ms</p>
          )}
        </div>
      </TooltipContent>
    </Tooltip>
  );
}

interface StatusTimelineProps {
  org: string;
  checkUid: string;
}

export function StatusTimeline({ org, checkUid }: StatusTimelineProps) {
  const { data, isLoading } = useQuery({
    queryKey: ["results", org, checkUid],
    queryFn: () => fetchResults(org, checkUid),
    refetchInterval: 60000, // Refresh every minute
  });

  if (isLoading) {
    return <Skeleton className="h-6 w-full" />;
  }

  const results = data?.data || [];

  // If no results, show empty state
  if (results.length === 0) {
    return (
      <div className="h-6 flex items-center justify-center">
        <span className="text-xs text-muted-foreground">No data available</span>
      </div>
    );
  }

  // Display up to 48 results (representing ~24 hours at 30min intervals)
  const displayResults = results.slice(0, 48).reverse();

  return (
    <div className="flex items-center gap-0.5 justify-end">
      {displayResults.map((result, index) => (
        <StatusBar
          key={result.uid || index}
          status={result.status}
          timestamp={result.periodStart}
          latency={result.durationMs}
        />
      ))}
    </div>
  );
}
