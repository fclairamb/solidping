import { useTranslation } from "react-i18next";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type { DailyAvailabilityPoint } from "@/api/hooks";

function getBarColor(status: string) {
  switch (status) {
    case "up":
      return "bg-green-500";
    case "degraded":
      return "bg-yellow-500";
    case "down":
      return "bg-red-500";
    default:
      return "bg-gray-300";
  }
}

interface AvailabilityBarProps {
  dailyAvailability: DailyAvailabilityPoint[];
  overallAvailabilityPct?: number;
  historyDays: number;
}

export function AvailabilityBar({
  dailyAvailability,
  overallAvailabilityPct,
  historyDays,
}: AvailabilityBarProps) {
  const { i18n } = useTranslation();

  const formatDate = (dateStr: string) => {
    const date = new Date(dateStr + "T00:00:00");
    return date.toLocaleDateString(i18n.language, {
      month: "short",
      day: "numeric",
    });
  };

  return (
    <div className="mt-2">
      <div className="flex gap-px">
        {dailyAvailability.map((point) => (
          <Tooltip key={point.date}>
            <TooltipTrigger asChild>
              <div
                className={`h-7 flex-1 rounded-sm ${getBarColor(point.status)} transition-opacity hover:opacity-80`}
              />
            </TooltipTrigger>
            <TooltipContent>
              <p className="font-medium">{formatDate(point.date)}</p>
              {point.status !== "noData" ? (
                <p className="text-xs">
                  {point.availabilityPct.toFixed(2)}% uptime
                </p>
              ) : (
                <p className="text-xs text-muted-foreground">No data</p>
              )}
            </TooltipContent>
          </Tooltip>
        ))}
      </div>
      <div className="mt-1 flex justify-between text-xs text-muted-foreground">
        <span>{historyDays} days ago</span>
        {overallAvailabilityPct != null && (
          <span className="font-medium text-foreground">
            {overallAvailabilityPct.toFixed(3)}% uptime
          </span>
        )}
        <span>Today</span>
      </div>
    </div>
  );
}
